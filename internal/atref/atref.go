// Package atref builds a map tab's graph.json as it existed at an
// arbitrary git ref (branch/tag/commit), not just the current working
// tree. It powers the "View a branch" / "Compare two refs" UI: resolve the
// ref to a commit, check that commit out into a throwaway DETACHED
// worktree, run the tab's own generator command against it, capture the
// resulting artifact, cache it by (tab, commit), and remove the worktree.
//
// The current working tree stays the fast path (ref == "" or "working"):
// no worktree, no cache, same generator run /api/update already does.
//
// Safety model: a ref is only ever used after gitmeta.VerifyRef resolves
// it to a real commit hash — every git call downstream (including "git
// worktree add") operates on that resolved SHA, never the raw ref text.
// Running a tab's generator against a historical ref carries the exact
// same trust model as running it against the current working tree (see
// internal/runner's doc comment): both execute a command the repo itself
// declared in its own committed observer.yaml.
package atref

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/taikuri-infra/observer/internal/gitmeta"
	"github.com/taikuri-infra/observer/internal/manifest"
	"github.com/taikuri-infra/observer/internal/runner"
)

// WorkingRef is the sentinel ref value (alongside "") that means "the
// current working tree" — GraphAtRef special-cases both to skip the
// worktree/cache machinery entirely.
const WorkingRef = "working"

// BuildTimeout bounds how long a single cache-miss build (worktree add +
// generator run) may run before it's canceled, so a hung analyzer — or a
// historical ref whose tooling spins forever — can't wedge the server. A
// package var (not a const) so tests can shrink it.
var BuildTimeout = 4 * time.Minute

// ErrInvalidRef wraps every error caused by a ref that doesn't resolve to a
// real commit, so callers (the /api/graphat handler) can tell "bad input"
// (400) apart from "the ref's code failed to build" (500) without parsing
// error strings.
var ErrInvalidRef = fmt.Errorf("invalid ref")

// GraphAtRef returns tab's graph.json (its declared Output file's bytes)
// as it existed at ref in repo.
//
//   - ref == "" or WorkingRef: the CURRENT working tree. Runs tab's
//     generator directly against repo — no worktree, no cache — equivalent
//     to the artifact /api/update + /api/artifact already produce.
//   - any other ref: resolved and validated via gitmeta.VerifyRef. A cache
//     hit (keyed by tab + resolved commit) returns immediately. A cache
//     miss builds in a throwaway detached worktree, then caches the
//     result.
func GraphAtRef(repo string, tab manifest.Tab, ref string) ([]byte, error) {
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return nil, fmt.Errorf("resolve repo path %q: %w", repo, err)
	}

	if ref == "" || ref == WorkingRef {
		return buildWorking(absRepo, tab)
	}

	sha, err := gitmeta.VerifyRef(absRepo, ref)
	if err != nil {
		return nil, fmt.Errorf("%w: %q does not resolve to a commit: %v", ErrInvalidRef, ref, err)
	}

	if b, ok := readCache(absRepo, tab, sha); ok {
		return b, nil
	}

	// Dedup concurrent identical (tab, sha) builds: two requests for the
	// same tab at the same commit share one build and one result, rather
	// than double-building or racing the same worktree path.
	key := tab.Name + "\x00" + tab.Output + "\x00" + sha
	return buildGroup.Do(key, func() ([]byte, error) {
		// Re-check: another goroutine may have populated the cache between
		// our miss above and acquiring the slot below.
		if b, ok := readCache(absRepo, tab, sha); ok {
			return b, nil
		}
		// A second lock, keyed by SHA ALONE (not tab+sha): two different
		// tabs resolving to the same commit still share one worktree path
		// (".observer-worktrees/<sha>"), so their add/run/remove lifecycles
		// must be serialized even though buildGroup's key doesn't dedup
		// them against each other.
		unlock := lockSHA(sha)
		defer unlock()
		return buildAtSHA(absRepo, tab, sha)
	})
}

// buildWorking runs tab's generator directly against repo (the real
// working tree, no worktree) and returns its declared Output file's
// bytes. Never cached: the working tree can change from one call to the
// next.
func buildWorking(repo string, tab manifest.Tab) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), BuildTimeout)
	defer cancel()
	if _, err := runner.Run(ctx, repo, "", tab, time.Now()); err != nil {
		return nil, fmt.Errorf("build tab %q from the working tree: %w", tab.Name, err)
	}
	b, err := os.ReadFile(filepath.Join(repo, "obs-data", tab.Output))
	if err != nil {
		return nil, fmt.Errorf("read generated artifact for tab %q: %w", tab.Name, err)
	}
	return b, nil
}

// buildAtSHA is the cache-miss path: create a detached worktree at sha,
// run tab's generator against it, read its Output file, cache the bytes,
// and unconditionally remove the worktree (even on error/timeout/panic).
// Called with sha already locked by the caller.
func buildAtSHA(repo string, tab manifest.Tab, sha string) (_ []byte, err error) {
	commonDir, err := gitCommonDir(repo)
	if err != nil {
		return nil, fmt.Errorf("resolve git common dir for %s: %w", repo, err)
	}
	worktreesRoot := filepath.Join(commonDir, ".observer-worktrees")
	if err := os.MkdirAll(worktreesRoot, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", worktreesRoot, err)
	}
	wtPath := filepath.Join(worktreesRoot, sha)

	// Defensive pre-cleanup: a prior crashed build may have left this exact
	// path registered as a worktree (or just lying on disk, if the process
	// died between "add" and a later step). Clear it first so "git worktree
	// add" never fails with "already exists" — and because sha is locked,
	// nothing else can be using it right now.
	cleanupWorktree(repo, wtPath)

	addCmd := gitmeta.Command(repo, "worktree", "add", "--detach", wtPath, sha)
	if out, addErr := addCmd.CombinedOutput(); addErr != nil {
		cleanupWorktree(repo, wtPath)
		return nil, fmt.Errorf("git worktree add %s %s: %w: %s", wtPath, sha, addErr, strings.TrimSpace(string(out)))
	}
	// Cleanup is mandatory and must run even if the generator panics: the
	// worktree must never outlive this function, and must never be left
	// registered for a future build at the same sha to collide with.
	defer cleanupWorktree(repo, wtPath)

	ctx, cancel := context.WithTimeout(context.Background(), BuildTimeout)
	defer cancel()

	if _, runErr := runner.Run(ctx, wtPath, "", tab, time.Now()); runErr != nil {
		return nil, fmt.Errorf("build tab %q at %s: generator failed: %w", tab.Name, sha, runErr)
	}

	outPath := filepath.Join(wtPath, "obs-data", tab.Output)
	b, readErr := os.ReadFile(outPath)
	if readErr != nil {
		return nil, fmt.Errorf("read generated artifact %s for tab %q at %s: %w", outPath, tab.Name, sha, readErr)
	}

	if cacheErr := writeCache(repo, tab, sha, b); cacheErr != nil {
		// Non-fatal: the build itself succeeded. Failing to persist the
		// cache just means the next request rebuilds instead of hitting it.
		_ = cacheErr
	}

	return b, nil
}

// cleanupWorktree removes wtPath as a git worktree (if registered),
// prunes any other stale worktree administrative data, and finally
// removes the directory outright as a belt-and-braces fallback — so a
// failed or interrupted build never leaves a stray worktree, and a repeat
// build at the same sha always starts from a clean slate. Errors are
// deliberately swallowed: this is cleanup, called from defer, and the
// directory not existing (the common case on the happy path before the
// first build) is not a failure.
func cleanupWorktree(repo, wtPath string) {
	_ = gitmeta.Command(repo, "worktree", "remove", "--force", wtPath).Run()
	_ = gitmeta.Command(repo, "worktree", "prune").Run()
	_ = os.RemoveAll(wtPath)
}

// gitCommonDir resolves repo's shared .git directory via `git
// rev-parse --git-common-dir`, which is correct whether repo is a normal
// repo (".git") or itself a linked worktree (whose ".git" is a file
// pointing elsewhere) — exactly where observer's own ephemeral worktrees
// and graph cache must live so they're never visible to `git status` and
// never collide with a repo that happens to have an obs-data/.graphcache
// of its own.
func gitCommonDir(repo string) (string, error) {
	out, err := gitmeta.Command(repo, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repo, dir)
	}
	return filepath.Clean(dir), nil
}

// cacheDir is where built graph.json bytes are persisted, keyed by tab and
// commit: <git-common-dir>/.observer-graphcache/. Deliberately NOT under
// <repo>/obs-data/ — that directory lives in the repo's working tree, and
// landing cache files there would make them show up in `git status` for
// any repo that doesn't happen to .gitignore obs-data/.graphcache itself.
// Everything under the git common dir is already invisible to git by
// construction, so this needs no .gitignore entry in any observed repo.
func cacheDir(repo string) (string, error) {
	commonDir, err := gitCommonDir(repo)
	if err != nil {
		return "", err
	}
	return filepath.Join(commonDir, ".observer-graphcache"), nil
}

func cachePath(repo string, tab manifest.Tab, sha string) (string, error) {
	dir, err := cacheDir(repo)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, cacheFileName(tab, sha)), nil
}

// cacheFileName keys the cache by tab (name + declared output, so two tabs
// that happen to share a name — shouldn't happen, manifest names are the
// lookup key elsewhere, but defense in depth — still don't collide) and
// the resolved commit SHA.
func cacheFileName(tab manifest.Tab, sha string) string {
	return fmt.Sprintf("%s__%s__%s.json", sanitizeForFilename(tab.Name), sanitizeForFilename(tab.Output), sha)
}

// sanitizeForFilename replaces every character that isn't safe to use
// verbatim in a filename with "_". Tab names are free-text ("Call graph
// (cmd/server)"); Output can contain "/" for a nested relative path.
func sanitizeForFilename(s string) string {
	if s == "" {
		return "_"
	}
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			// keep as-is
		default:
			b[i] = '_'
		}
	}
	return string(b)
}

func readCache(repo string, tab manifest.Tab, sha string) ([]byte, bool) {
	p, err := cachePath(repo, tab, sha)
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	return b, true
}

// writeCache persists b atomically (write a temp file, then rename) so a
// reader never observes a partially-written cache file.
func writeCache(repo string, tab manifest.Tab, sha string, b []byte) error {
	p, err := cachePath(repo, tab, sha)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// buildGroup dedups concurrent GraphAtRef calls for the same (tab, sha):
// the first caller actually builds, every concurrent caller for the same
// key blocks on it and receives the same result, instead of each starting
// its own worktree build.
var buildGroup = newCallGroup()

// callGroup is a minimal keyed call-dedup primitive (the same shape as
// golang.org/x/sync/singleflight's core, written out here rather than
// adding a dependency for one use site): Do runs fn for key at most once
// among concurrent callers; every caller — the one that ran fn and every
// one that arrived while it was running — gets fn's result.
type callGroup struct {
	mu    sync.Mutex
	calls map[string]*call
}

type call struct {
	wg  sync.WaitGroup
	val []byte
	err error
}

func newCallGroup() *callGroup {
	return &callGroup{calls: make(map[string]*call)}
}

func (g *callGroup) Do(key string, fn func() ([]byte, error)) ([]byte, error) {
	g.mu.Lock()
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &call{}
	c.wg.Add(1)
	g.calls[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()

	return c.val, c.err
}

// shaLocks serializes the worktree lifecycle (pre-cleanup -> add -> run ->
// remove) per commit SHA, independent of buildGroup's per-(tab,sha) dedup:
// two DIFFERENT tabs that resolve to the SAME commit still share one
// worktree directory (".observer-worktrees/<sha>"), so their builds must
// never overlap. Entries are intentionally never removed — this is a
// long-running local dev server touching a bounded, small set of distinct
// commits per session, not an unbounded cache.
var shaLocks sync.Map // map[string]*sync.Mutex

func lockSHA(sha string) func() {
	v, _ := shaLocks.LoadOrStore(sha, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}
