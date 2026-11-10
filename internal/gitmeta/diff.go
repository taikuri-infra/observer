package gitmeta

import (
	"fmt"
	"strconv"
	"strings"
)

// Hunk is one changed region of a file, working-tree side: [startLine,
// lineCount] plus the number of lines the hunk REMOVED from the base side
// (oldCount). oldCount lets a consumer tell a pure insertion (oldCount ==
// 0 — brand-new lines, evidence a function was "added") from a rewrite
// (oldCount > 0 — lines replaced, so the region "changed", not "added")
// even though both only ever report new-side line numbers.
type Hunk struct {
	Start    int `json:"start"`
	Count    int `json:"count"`
	OldCount int `json:"oldCount"`
}

// DiffFile is one changed file in a DiffAgainst result: its status
// collapsed to the three the git-aware overlay distinguishes ("A" added,
// "M" modified, "D" deleted — renames/copies/type-changes collapse to "M"
// at their destination path: the file now exists with different content,
// not as a brand-new node), plus its working-tree-side changed-line ranges.
type DiffFile struct {
	Status string `json:"status"`
	Hunks  []Hunk `json:"hunks,omitempty"` // working-tree side; omitted for deletes
}

// DiffAgainst computes dir's diff between mergeBase and the WORKING TREE:
// the same comparison ChangedGraphModel/Changes already make (a plain
// `git diff <mergeBase>` diffs the index+worktree against that commit, so
// uncommitted edits are included), but file-granular with per-file
// working-tree-side added/changed line ranges (DiffFile.Hunks) — the data
// GET /api/gitdiff serves for the git-aware overlay's
// added/changed/unchanged node classification, function-precise where a
// node has a line range to compare against. Untracked-but-not-ignored
// files are folded in as synthetic adds (status "A") so a brand-new,
// never-committed file's nodes light up as added rather than reading
// unchanged. mergeBase must already be resolved by the caller (e.g.
// MergeBase against VerifyRef's returned hash) — DiffAgainst doesn't
// re-derive it.
func DiffAgainst(dir, mergeBase string) (map[string]DiffFile, error) {
	files := map[string]DiffFile{}

	nameStatus, err := git(dir, "diff", "--no-ext-diff", "--name-status", mergeBase)
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

	// Untracked (new, uncommitted, not .gitignore'd) files never appear in
	// `git diff <commit>` — they aren't in the index. Fold them in as
	// synthetic adds so their nodes classify as "added", not "unchanged".
	// A file already seen above (shouldn't happen for untracked, but be
	// defensive) is not overwritten.
	untracked, err := git(dir, "ls-files", "--others", "--exclude-standard")
	if err == nil {
		for _, line := range strings.Split(untracked, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if _, ok := files[line]; !ok {
				files[line] = DiffFile{Status: "A"}
			}
		}
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

// diffHunksByFile runs `git diff --no-ext-diff --unified=0 mergeBase` and
// collects each file's working-tree-side hunk ranges from unified diff's
// own "@@ -oldStart,oldCount +newStart,newCount @@" headers: Start/Count
// from the "+" side, OldCount from the "-" side (so a consumer can tell a
// pure insertion from a rewrite — see Hunk). --unified=0 means every hunk
// is purely the changed lines themselves, no surrounding context, so these
// ranges are exactly "added or changed on the working-tree side" with no
// padding. A deletion-only hunk (newCount == 0: lines removed, nothing
// added at that position) is dropped — there's nothing on the new side to
// highlight there.
func diffHunksByFile(dir, mergeBase string) (map[string][]Hunk, error) {
	out, err := git(dir, "diff", "--no-ext-diff", "--unified=0", mergeBase)
	if err != nil {
		return nil, fmt.Errorf("git diff --unified=0 %s: %w", mergeBase, err)
	}
	hunks := map[string][]Hunk{}
	var current string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			current = parseDiffPlusPath(line)
		case strings.HasPrefix(line, "@@ "):
			if current == "" {
				continue
			}
			h, ok := parseHunkHeader(line)
			if !ok || h.Count == 0 {
				continue
			}
			hunks[current] = append(hunks[current], h)
		}
	}
	return hunks, nil
}

// parseDiffPlusPath extracts the new-side path from a unified diff's
// "+++ b/<path>" header line ("+++ /dev/null" for a deleted file, which
// has no new-side lines to report and is returned as "").
func parseDiffPlusPath(line string) string {
	p := strings.TrimPrefix(line, "+++ ")
	p = strings.TrimRight(p, "\r\n")
	if p == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(p, "b/")
}

// parseHunkHeader parses a "@@ -oldStart[,oldCount] +newStart[,newCount] @@"
// line into a Hunk: new-side Start/Count (+), old-side OldCount (-). A bare
// "+N"/"-N" (no comma) means count 1, per unified diff's own convention.
func parseHunkHeader(line string) (Hunk, bool) {
	var h Hunk
	var haveNew, haveOld bool
	for _, p := range strings.Fields(line) {
		if strings.HasPrefix(p, "+") {
			s, c, ok := parseRangePart(strings.TrimPrefix(p, "+"))
			if !ok {
				return Hunk{}, false
			}
			h.Start, h.Count = s, c
			haveNew = true
		} else if strings.HasPrefix(p, "-") {
			_, c, ok := parseRangePart(strings.TrimPrefix(p, "-"))
			if !ok {
				return Hunk{}, false
			}
			h.OldCount = c
			haveOld = true
		}
	}
	if !haveNew || !haveOld {
		return Hunk{}, false
	}
	return h, true
}

// parseRangePart parses a unified-diff range token's body ("start[,count]",
// the part after the leading +/-), defaulting count to 1 when omitted.
func parseRangePart(body string) (start, count int, ok bool) {
	nums := strings.SplitN(body, ",", 2)
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
