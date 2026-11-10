package gen

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/taikuri-infra/observer/internal/gitmeta"
)

// Marker is one TODO/FIXME/HACK/XXX/BUG comment found by TODOs.
type Marker struct {
	Marker string `json:"marker"` // TODO | FIXME | HACK | XXX | BUG
	Text   string `json:"text"`   // the comment text after the marker, trimmed
	Path   string `json:"path"`   // repo-relative, forward slashes
	Line   int    `json:"line"`
}

// markerRe matches a marker keyword as a standalone word, optionally
// followed by ":", "(name):", or whitespace, then captures the rest of the
// line as the marker's text. Matching is case-SENSITIVE and whole-word only
// (via \b) — this is a pragmatic, language-agnostic contract: it does NOT
// verify the match sits inside a comment, so a string literal or identifier
// that happens to contain one of these words as a standalone token (e.g. a
// variable named just "TODO", or a commit message quoted in a string) would
// be a false positive. In practice marker words essentially never appear as
// bare identifiers/literals outside comments, so this tradeoff is accepted
// rather than writing a per-language comment grammar.
var markerRe = regexp.MustCompile(`\b(TODO|FIXME|HACK|XXX|BUG)\b[:\s(]?\s*(.*)`)

// markerPriority orders markers from most to least urgent for sorting:
// bugs/fixmes first, then hacks/odd-code flags, then plain todos.
var markerPriority = map[string]int{
	"FIXME": 0,
	"BUG":   1,
	"HACK":  2,
	"XXX":   3,
	"TODO":  4,
}

// todosSourceExt is the set of file extensions TODOs scans. This keeps the
// scan to text/source files and avoids wasting time (or risking a false
// "binary" misread) on assets that would never contain a marker comment
// anyway.
var todosSourceExt = map[string]bool{
	".go":    true,
	".js":    true,
	".ts":    true,
	".tsx":   true,
	".jsx":   true,
	".css":   true,
	".scss":  true,
	".astro": true,
	".md":    true,
	".yaml":  true,
	".yml":   true,
	".json":  true,
	".sh":    true,
	".sql":   true,
	".html":  true,
	".txt":   true,
	".mod":   true,
}

// maxMarkerTextLen caps the captured comment text so a pathological
// single-line file (e.g. minified JS with a stray "TODO" token) can't blow
// up the output.
const maxMarkerTextLen = 200

// TODOs scans repoDir's git-tracked text files for TODO/FIXME/HACK/XXX/BUG
// markers and returns them sorted by (marker priority, path, line). top<=0
// means unlimited.
//
// Only files git considers tracked (via `git ls-files`) are scanned, which
// automatically respects .gitignore and keeps generated/vendored/
// node_modules trees out of the report without any extra filtering. Files
// are further filtered to a known source/text extension allowlist
// (todosSourceExt); anything else is skipped. A file that can't be opened or
// read is skipped rather than failing the whole scan.
func TODOs(repoDir string, top int) ([]Marker, error) {
	// gitmeta.Command hardens the invocation against malicious repo-local
	// config/hooks — TODOs may run against an untrusted mounted repo.
	cmd := gitmeta.Command(repoDir, "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git ls-files in %s: %w\n%s", repoDir, err, ee.Stderr)
		}
		return nil, fmt.Errorf("git ls-files in %s: %w", repoDir, err)
	}

	var markers []Marker
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		if !todosSourceExt[strings.ToLower(filepath.Ext(rel))] {
			continue
		}
		found, err := scanFileForMarkers(repoDir, rel)
		if err != nil {
			// Skip unreadable files rather than aborting the whole scan — a
			// broken symlink or permission issue shouldn't hide every other
			// marker.
			continue
		}
		markers = append(markers, found...)
	}

	sort.Slice(markers, func(i, j int) bool {
		pi, pj := markerPriority[markers[i].Marker], markerPriority[markers[j].Marker]
		if pi != pj {
			return pi < pj
		}
		if markers[i].Path != markers[j].Path {
			return markers[i].Path < markers[j].Path
		}
		return markers[i].Line < markers[j].Line
	})
	if top > 0 && len(markers) > top {
		markers = markers[:top]
	}
	return markers, nil
}

// scanFileForMarkers reads repoDir/rel line by line looking for marker
// comments. It bails out (returning no markers, no error) on the first
// binary-looking chunk — a NUL byte — rather than scanning a binary file
// line-by-line.
func scanFileForMarkers(repoDir, rel string) ([]Marker, error) {
	f, err := os.Open(filepath.Join(repoDir, rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	relSlash := filepath.ToSlash(rel)

	var markers []Marker
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if bytes.IndexByte(line, 0) >= 0 {
			// Binary content masquerading under an allowed extension — bail
			// out of this file entirely.
			return nil, nil
		}
		m := markerRe.FindStringSubmatch(string(line))
		if m == nil {
			continue
		}
		text := strings.TrimSpace(m[2])
		if len(text) > maxMarkerTextLen {
			text = text[:maxMarkerTextLen]
		}
		markers = append(markers, Marker{
			Marker: m[1],
			Text:   text,
			Path:   relSlash,
			Line:   lineNo,
		})
	}
	if err := sc.Err(); err != nil {
		return markers, err
	}
	return markers, nil
}
