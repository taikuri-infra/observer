// Package atref builds a map tab's graph.json as it existed at an
// arbitrary git ref (branch/tag/commit), not just the current working
// tree. It powers the "View a branch" / "Compare two refs" UI: resolve the
// ref to a commit, check that commit out into a throwaway DETACHED
// worktree, run the tab's own generator command against it, capture the
// resulting artifact, cache it by (tab, commit), and remove the worktree.
//
// The current working tree stays the fast path (ref == "" or "working"):
// it serves the EXISTING obs-data/<output> artifact as-is (no generator
// run, no worktree, no cache) — the same bytes /api/artifact returns.
//
// Safety model: a ref is only ever used after gitmeta.VerifyRef resolves
// it to a real commit hash — every git call downstream (including "git
// worktree add") operates on that resolved SHA, never the raw ref text.
// Running a tab's generator against a historical ref carries the exact
// same trust model as running it against the current working tree (see
// internal/runner's doc comment): both execute a command the repo itself
// declared in its own committed observer.yaml.
//
// Residual threat (bounded, documented): `git worktree add` performs a
// CHECKOUT, and a repo-local checkout filter (core.autocrlf is harmless,
// but a `filter.<name>.smudge` / `.process` keyed from .gitattributes, or
// Git-LFS) runs an arbitrary program as it materializes working-tree
// files. gitmeta.Command's hardening neutralizes hook/diff/ext RCE sinks
// but does NOT clear smudge filters (they're keyed per-attribute, not a
// single config key we can blank). This is the SAME trust class as
// running the repo's declared generators (the whole tool's premise), and
// it's bounded two ways: the checkout runs under BuildTimeout (a hung or
// wedging filter is killed), and GIT_LFS_SKIP_SMUDGE=1 is set so the
// common LFS filter no-ops. A maximally hostile repo could still ship a
// custom smudge filter; that's accepted, same as its generator commands.
package atref

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// Cache-eviction caps for .observer-graphcache/: after each successful
// build, the oldest entries (by mtime) are deleted once the directory
// exceeds EITHER bound. The cache is invisible (inside .git) and otherwise
// unbounded, so without this a long-running server would grow it forever.
// Vars (not consts) so tests can shrink them.
var (
	cacheMaxBytes int64 = 200 << 20 // ~200 MiB
	cacheMaxFiles       = 500
)

// maxConcurrentBuilds bounds how many cache-miss worktree checkouts run at
// once: N distinct refs requested together would otherwise fan out to N
// full `git worktree add` checkouts hammering the disk. A small semaphore
// keeps that bounded without serializing everything.
const maxConcurrentBuilds = 3

var buildSem = make(chan struct{}, maxConcurrentBuilds)

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
		return serveWorking(absRepo, tab)
	}

	sha, err := gitmeta.VerifyRef(absRepo, ref)
	if err != nil {
		return nil, fmt.Errorf("%w: %q does not resolve to a commit: %v", ErrInvalidRef, ref, err)
	}

	// One-time sweep per repo (before the first worktree build) clears any
	// full checkout a prior SIGKILL'd process left stranded under
	// .observer-worktrees/ — pre-cleanup in buildAtSHA only heals the SAME
	// sha, so without this a crashed build of sha X leaks X's checkout
	// forever once nothing requests X again.
	sweepRepoOnce(absRepo)

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
		// them against each other. Lock is acquired BEFORE the build
		// semaphore so a goroutine waiting on the lock doesn't hold a
		// semaphore slot.
		unlock := lockSHA(sha)
		defer unlock()
		buildSem <- struct{}{}
		defer func() { <-buildSem }()
		return buildAtSHA(absRepo, tab, sha)
	})
}

// serveWorking returns the EXISTING working-tree artifact
// (repo/obs-data/<output>) as-is — the same bytes /api/artifact serves. It
// deliberately does NOT run the generator: a GET must not mutate the
// working tree (that's POST /api/update's job) and must not race a
// concurrent /api/update writing the same file. If the artifact hasn't
// been generated yet, that's a clear error the caller surfaces, not a
// silent rebuild.
func serveWorking(repo string, tab manifest.Tab) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(repo, "obs-data", tab.Output))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("artifact for tab %q not generated yet — run Update first", tab.Name)
		}
		return nil, fmt.Errorf("read working-tree artifact for tab %q: %w", tab.Name, err)
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

	// A single timeout bounds BOTH the checkout AND the generator run, so a
	// hung `git worktree add` (slow disk, or a wedging repo-local smudge
	// filter — see the package doc's residual-threat note) can't block
	// forever while holding the shaLock + semaphore slot. Start it before
	// the add.
	ctx, cancel := context.WithTimeout(context.Background(), BuildTimeout)
	defer cancel()

	// Defensive pre-cleanup: a prior crashed build may have left this exact
	// path registered as a worktree (or just lying on disk, if the process
	// died between "add" and a later step). Clear it first so "git worktree
	// add" never fails with "already exists" — and because sha is locked,
	// nothing else can be using it right now.
	cleanupWorktree(repo, wtPath)

	if out, addErr := worktreeAdd(ctx, repo, wtPath, sha); addErr != nil {
		cleanupWorktree(repo, wtPath)
		return nil, fmt.Errorf("git worktree add %s %s: %w: %s", wtPath, sha, addErr, strings.TrimSpace(string(out)))
	}
	// Cleanup is mandatory and must run even if the generator panics: the
	// worktree must never outlive this function, and must never be left
	// registered for a future build at the same sha to collide with.
	defer cleanupWorktree(repo, wtPath)

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
	} else if dir, derr := cacheDir(repo); derr == nil {
		evictCache(dir, cacheMaxBytes, cacheMaxFiles)
	}

	return b, nil
}

// worktreeAdd runs `git worktree add --detach <wtPath> <sha>` under ctx
// (so BuildTimeout bounds the checkout), reusing gitmeta.Command's full
// hardening (no hooks/fsmonitor/ext, pinned config) by reconstructing the
// same argv+env on a context-bound exec.Cmd — exec.CommandContext has no
// hardened constructor of its own. GIT_LFS_SKIP_SMUDGE=1 no-ops the common
// Git-LFS smudge filter on checkout (see the package doc's residual note).
func worktreeAdd(ctx context.Context, repo, wtPath, sha string) ([]byte, error) {
	base := gitmeta.Command(repo, "worktree", "add", "--detach", wtPath, sha)
	cmd := exec.CommandContext(ctx, base.Path, base.Args[1:]...)
	cmd.Dir = base.Dir
	cmd.Env = append(base.Env, "GIT_LFS_SKIP_SMUDGE=1")
	return cmd.CombinedOutput()
}

// cleanupWorktree removes wtPath as a git worktree (if registered), then
// removes its directory outright, and only THEN prunes — so a failed or
// interrupted build never leaves a stray worktree, and a repeat build at
// the same sha always starts from a clean slate.
//
// Ordering matters: `git worktree remove` + `os.RemoveAll` target OUR
// path specifically, but `git worktree prune` is repo-GLOBAL — it
// deregisters every worktree whose directory is currently missing. Doing
// our RemoveAll FIRST means prune then reaps exactly our now-gone entry;
// doing prune first, while another of the user's OWN worktrees happened to
// be transiently absent, could deregister theirs. Errors are deliberately
// swallowed: this is cleanup, called from defer, and the directory not
// existing (the happy path before the first build) is not a failure.
func cleanupWorktree(repo, wtPath string) {
	_ = gitmeta.Command(repo, "worktree", "remove", "--force", wtPath).Run()
	_ = os.RemoveAll(wtPath)
	_ = gitmeta.Command(repo, "worktree", "prune").Run()
}

// sweepRepoOnce runs sweepWorktrees(repo) exactly once per repo per
// process — before the first worktree build — to clear any full checkout a
// prior SIGKILL'd process stranded under .observer-worktrees/. Concurrent
// first-callers all block on the same once, so the sweep completes before
// any of them starts a build.
var repoSweeps sync.Map // map[string]*sync.Once

func sweepRepoOnce(repo string) {
	v, _ := repoSweeps.LoadOrStore(repo, &sync.Once{})
	v.(*sync.Once).Do(func() { sweepWorktrees(repo) })
}

// sweepWorktrees removes the entire .observer-worktrees/ tree for repo and
// prunes, reaping every ephemeral checkout a previous run left behind.
// Safe because everything under .observer-worktrees/ is ours by
// construction, and it's gated to run before any build in this process so
// it never races a live one.
func sweepWorktrees(repo string) {
	commonDir, err := gitCommonDir(repo)
	if err != nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(commonDir, ".observer-worktrees"))
	_ = gitmeta.Command(repo, "worktree", "prune").Run()
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

// cacheFileName keys the cache by the resolved commit SHA plus a sha256
// hex digest of (tab.Name, tab.Output). The digest — not a sanitized
// concatenation — is what makes the key UNAMBIGUOUS: sanitizing + joining
// with "__" could alias distinct tabs onto one file (e.g. name "A B" and
// name "A_B" both sanitize to "A_B", or an output boundary landing inside
// a name already containing "__"), serving one tab's cache for another.
// A length-prefixed hash of the two fields has no such collision.
func cacheFileName(tab manifest.Tab, sha string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s%d:%s", len(tab.Name), tab.Name, len(tab.Output), tab.Output)
	return fmt.Sprintf("%s__%s.json", sha, hex.EncodeToString(h.Sum(nil))[:32])
}

// evictCache deletes the oldest entries (by mtime) in dir once it exceeds
// either maxBytes or maxFiles, so the otherwise-unbounded, git-invisible
// cache can't grow without limit. Best-effort: errors are ignored (it runs
// after a successful build; a failed eviction just defers the next sweep).
func evictCache(dir string, maxBytes int64, maxFiles int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fileInfo struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []fileInfo
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".tmp") {
			continue // a concurrent write-in-progress; leave it alone
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		files = append(files, fileInfo{filepath.Join(dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	count := len(files)
	if total <= maxBytes && count <= maxFiles {
		return
	}
	// Oldest first: evict until BOTH caps are satisfied.
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, f := range files {
		if total <= maxBytes && count <= maxFiles {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
			count--
		}
	}
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

func (g *callGroup) Do(key string, fn func() ([]byte, error)) (val []byte, err error) {
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

	// wg.Done and the map delete MUST run even if fn panics — otherwise
	// every waiter already blocked on c.wg.Wait() blocks forever, AND the
	// entry stays in g.calls so every FUTURE caller for this key deadlocks
	// on the same stale WaitGroup. The recover converts the panic into an
	// error returned to all waiters (via c.err) AND to THIS leader (via the
	// named returns — an unnamed-return func whose deferred recover fires
	// would otherwise hand the leader zero values, nil error included).
	defer func() {
		if r := recover(); r != nil {
			c.err = fmt.Errorf("build panicked: %v", r)
		}
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		c.wg.Done()
		val, err = c.val, c.err
	}()

	c.val, c.err = fn()
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
