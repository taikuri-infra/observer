package gitmeta

import (
	"fmt"
	"strconv"
	"strings"
)

// DiffFile is one changed file in a DiffAgainst result: its status
// collapsed to the three the git-aware overlay distinguishes ("A" added,
// "M" modified, "D" deleted — renames/copies/type-changes collapse to "M"
// at their destination path: the file now exists with different content,
// not as a brand-new node), plus its HEAD-side changed-line ranges.
type DiffFile struct {
	Status string   `json:"status"`
	Hunks  [][2]int `json:"hunks,omitempty"` // [startLine, lineCount] pairs, HEAD-side; omitted for deletes
}

// DiffAgainst computes dir's diff between mergeBase and HEAD: the same
// comparison ChangedGraphModel/Changes already make, but file-granular
// with HEAD-side added/changed line ranges per file (DiffFile.Hunks) — the
// data GET /api/gitdiff serves for the git-aware overlay's
// added/changed/unchanged node classification, function-precise where a
// node has a line range to compare against. mergeBase must already be
// resolved by the caller (e.g. MergeBase, after VerifyRef has confirmed
// the user-supplied base ref itself resolves) — DiffAgainst doesn't
// re-derive it.
func DiffAgainst(dir, mergeBase string) (map[string]DiffFile, error) {
	files := map[string]DiffFile{}

	nameStatus, err := git(dir, "diff", "--name-status", mergeBase)
	if err != nil {
		return nil, fmt.Errorf("git diff --name-status %s: %w", mergeBase, err)
	}
	for _, line := range strings.Split(nameStatus, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		status := normalizeDiffStatus(fields[0])
		path := fields[len(fields)-1] // rename/copy: the destination path
		files[path] = DiffFile{Status: status}
	}

	hunks, err := diffHunksByFile(dir, mergeBase)
	if err != nil {
		return nil, err
	}
	for path, h := range hunks {
		f, ok := files[path]
		if !ok {
			continue // a hunk for a file --name-status didn't report: shouldn't happen, ignore defensively
		}
		f.Hunks = h
		files[path] = f
	}
	return files, nil
}

// normalizeDiffStatus collapses `git diff --name-status`'s full status
// vocabulary (A, M, D, Rnnn, Cnnn, T, U, X, B) to the three the overlay
// distinguishes — anything that isn't a bare add or delete is treated as
// "M" (the file exists, with different content, at its reported path).
func normalizeDiffStatus(raw string) string {
	if raw == "" {
		return "M"
	}
	switch raw[0] {
	case 'A':
		return "A"
	case 'D':
		return "D"
	default:
		return "M"
	}
}

// diffHunksByFile runs `git diff --unified=0 mergeBase` and collects each
// file's HEAD-side hunk ranges: the [startLine, lineCount] pairs unified
// diff's own "@@ -oldStart,oldCount +newStart,newCount @@" headers report
// for the "+" (HEAD) side. --unified=0 means every hunk is purely the
// changed lines themselves, no surrounding context, so these ranges are
// exactly "added or changed on HEAD" with no padding. A deletion-only
// hunk (newCount == 0: lines removed, nothing added at that position) is
// dropped — there's nothing on the HEAD side to highlight there.
func diffHunksByFile(dir, mergeBase string) (map[string][][2]int, error) {
	out, err := git(dir, "diff", "--unified=0", mergeBase)
	if err != nil {
		return nil, fmt.Errorf("git diff --unified=0 %s: %w", mergeBase, err)
	}
	hunks := map[string][][2]int{}
	var current string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			current = parseDiffPlusPath(line)
		case strings.HasPrefix(line, "@@ "):
			if current == "" {
				continue
			}
			start, count, ok := parseHunkHeader(line)
			if !ok || count == 0 {
				continue
			}
			hunks[current] = append(hunks[current], [2]int{start, count})
		}
	}
	return hunks, nil
}

// parseDiffPlusPath extracts the HEAD-side path from a unified diff's
// "+++ b/<path>" header line ("+++ /dev/null" for a deleted file, which
// has no HEAD-side lines to report and is returned as "").
func parseDiffPlusPath(line string) string {
	p := strings.TrimPrefix(line, "+++ ")
	p = strings.TrimRight(p, "\r\n")
	if p == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(p, "b/")
}

// parseHunkHeader parses a "@@ -oldStart[,oldCount] +newStart[,newCount] @@"
// line, returning the HEAD-side (+) start/count. A bare "+N" (no comma)
// means count 1, per unified diff's own convention.
func parseHunkHeader(line string) (start, count int, ok bool) {
	for _, p := range strings.Fields(line) {
		if !strings.HasPrefix(p, "+") {
			continue
		}
		nums := strings.SplitN(strings.TrimPrefix(p, "+"), ",", 2)
		s, err := strconv.Atoi(nums[0])
		if err != nil {
			return 0, 0, false
		}
		c := 1
		if len(nums) == 2 {
			c, err = strconv.Atoi(nums[1])
			if err != nil {
				return 0, 0, false
			}
		}
		return s, c, true
	}
	return 0, 0, false
}
