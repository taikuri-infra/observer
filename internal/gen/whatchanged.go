package gen

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/taikuri-infra/observer/internal/gitmeta"
)

// routeVerbRe matches a Fiber (gofiber/fiber v2) route-registration call's
// leading text — `.Get("/`, `.Post("/`, etc — on a single unified-diff line.
// It's a line-level heuristic, not a parser: it's applied to raw "+"/"-"
// diff lines by routeDelta below, so it can't tell a genuine route
// registration from, say, a commented-out line or a string that happens to
// contain the same text. Good enough to flag "routes probably changed here,
// go look", which is the point of an approximate delta.
var routeVerbRe = regexp.MustCompile(`\.(Get|Post|Put|Patch|Delete|All)\("/`)

// WhatChanged produces a Markdown digest of how repoDir's working tree
// (HEAD + uncommitted) differs from base, oriented for code review / an AI
// reviewer: a diffstat summary, changed Go packages, changed areas by
// top-level dir, new DB migrations (schema-change signal), and an
// approximate API-route delta. All sections are best-effort; a base that
// doesn't resolve or has no merge-base degrades to a clear note, never an
// error (match Changes' tolerance).
func WhatChanged(repoDir, base string) (string, error) {
	var b strings.Builder
	b.WriteString("# What changed\n\n")

	resolved, err := gitmeta.VerifyRef(repoDir, base)
	if err != nil {
		fmt.Fprintf(&b, "> Base `%s` not found in this repo — nothing to compare against.\n", base)
		return b.String(), nil
	}
	mergeBase, _ := gitmeta.MergeBase(repoDir, resolved, "HEAD")
	if mergeBase == "" {
		fmt.Fprintf(&b, "> No common history with `%s` — this branch and `%s` share no merge-base "+
			"(unrelated histories, or the ref doesn't exist as a local branch/commit this repo can trace to).\n", base, base)
		return b.String(), nil
	}

	diffFiles, err := gitmeta.DiffAgainst(repoDir, mergeBase)
	if err != nil {
		return "", fmt.Errorf("diff against merge-base: %w", err)
	}

	var paths []string
	for p := range diffFiles {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	branch, _ := gitOutput(repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	status, _ := gitOutput(repoDir, "status", "--porcelain")
	dirty := strings.TrimSpace(status) != ""
	aheadOut, _ := gitOutput(repoDir, "rev-list", "--count", mergeBase+"..HEAD")
	ahead, _ := strconv.Atoi(strings.TrimSpace(aheadOut))
	shortstat, _ := gitOutput(repoDir, "diff", "--shortstat", mergeBase)

	shortBase := mergeBase
	if len(shortBase) > 9 {
		shortBase = shortBase[:9]
	}

	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Branch | `%s` |\n", branch)
	fmt.Fprintf(&b, "| Compared against | `%s` (merge-base `%s`) |\n", base, shortBase)
	fmt.Fprintf(&b, "| Commits ahead of merge-base | %d |\n", ahead)
	fmt.Fprintf(&b, "| Files changed (incl. uncommitted) | %d |\n", len(paths))
	fmt.Fprintf(&b, "| Diffstat | %s |\n", orNone(strings.TrimSpace(shortstat)))
	fmt.Fprintf(&b, "| Working tree | %s |\n\n", dirtyLabel(dirty))

	// Changed files by area: group by top-level dir segment, same grouping
	// rule as Changes' changedByModule (services/<x>, apps/<x>, else the
	// first path segment), counted desc.
	areaCounts := map[string]int{}
	var areaOrder []string
	for _, p := range paths {
		a := areaOf(p)
		if areaCounts[a] == 0 {
			areaOrder = append(areaOrder, a)
		}
		areaCounts[a]++
	}
	sort.Strings(areaOrder)
	sortByCountDesc(areaOrder, areaCounts)
	const maxAreas = 15
	truncatedAreas := len(areaOrder) > maxAreas
	if truncatedAreas {
		areaOrder = areaOrder[:maxAreas]
	}
	b.WriteString("## Changed files by area\n\n```\n")
	if len(areaOrder) == 0 {
		b.WriteString("(none)")
	} else {
		for _, a := range areaOrder {
			fmt.Fprintf(&b, "%6d %s\n", areaCounts[a], a)
		}
		if truncatedAreas {
			b.WriteString("...\n")
		}
	}
	b.WriteString("\n```\n\n")

	// Changed Go packages: the directory of every changed .go file,
	// repo-relative — these are the packages a reviewer/AI should focus on.
	pkgSet := map[string]bool{}
	var pkgOrder []string
	for _, p := range paths {
		if !strings.HasSuffix(p, ".go") {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(p))
		if dir == "." {
			dir = "(root)"
		}
		if !pkgSet[dir] {
			pkgSet[dir] = true
			pkgOrder = append(pkgOrder, dir)
		}
	}
	sort.Strings(pkgOrder)
	const maxPkgs = 30
	truncatedPkgs := len(pkgOrder) > maxPkgs
	if truncatedPkgs {
		pkgOrder = pkgOrder[:maxPkgs]
	}
	b.WriteString("## Changed Go packages\n\n```\n")
	if len(pkgOrder) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, p := range pkgOrder {
			fmt.Fprintf(&b, "%s\n", p)
		}
		if truncatedPkgs {
			b.WriteString("...\n")
		}
	}
	b.WriteString("```\n\n")

	// New migrations: added files under a "migration" path ending .sql —
	// usually means new/changed tables, a schema-change signal worth
	// flagging loudly.
	var migrations []string
	for _, p := range paths {
		f := diffFiles[p]
		if f.Status == "A" && strings.Contains(strings.ToLower(p), "migration") && strings.HasSuffix(p, ".sql") {
			migrations = append(migrations, p)
		}
	}
	b.WriteString("## ⚠️ Schema changes (new migrations)\n\n```\n")
	if len(migrations) == 0 {
		b.WriteString("none\n")
	} else {
		for _, m := range migrations {
			fmt.Fprintf(&b, "%s\n", m)
		}
	}
	b.WriteString("```\n\n")

	// API route delta (approximate): line-level count of added/removed
	// Fiber verb-registration lines in any changed router-looking file.
	// This is NOT a semantic route diff — it can't tell a genuine route
	// registration from a commented-out line, a moved/reformatted call
	// (counts as one remove + one add), or a route whose path literal
	// changed (also one remove + one add, not a "rename"). Treat it as a
	// signal to go look at the router diff yourself, not a ground truth.
	var routerFiles []string
	for _, p := range paths {
		if strings.Contains(strings.ToLower(p), "router") {
			routerFiles = append(routerFiles, p)
		}
	}
	added, removed := 0, 0
	for _, f := range routerFiles {
		a, r := routeDelta(repoDir, mergeBase, f)
		added += a
		removed += r
	}
	b.WriteString("## API route delta (approximate)\n\n")
	if len(routerFiles) == 0 {
		b.WriteString("no route files changed\n\n")
	} else {
		fmt.Fprintf(&b, "+%d route(s), -%d route(s) (approximate, from the router diff of %s — "+
			"a line-level count of added/removed Fiber verb-call lines, not a semantic route diff)\n\n",
			added, removed, strings.Join(routerFiles, ", "))
	}

	// What to review: synthesize 3-6 bullets from the sections above.
	var bullets []string
	if len(migrations) > 0 {
		bullets = append(bullets, fmt.Sprintf(
			"Schema changed — %d new migration(s); check them plus any handlers/queries touching the affected tables.",
			len(migrations)))
	}
	if len(pkgOrder) > 0 {
		bullets = append(bullets, fmt.Sprintf("%d Go package(s) touched — focus review there first.", len(pkgOrder)))
	}
	if frontendAreas, frontendCount := areasWithPrefix(areaOrder, areaCounts, "apps/"); len(frontendAreas) > 0 {
		bullets = append(bullets, fmt.Sprintf("Frontend changes in %s (%d file(s) total).",
			strings.Join(frontendAreas, ", "), frontendCount))
	}
	if backendAreas, backendCount := areasWithPrefix(areaOrder, areaCounts, "services/"); len(backendAreas) > 0 {
		bullets = append(bullets, fmt.Sprintf("Backend changes in %s (%d file(s) total).",
			strings.Join(backendAreas, ", "), backendCount))
	}
	if len(routerFiles) > 0 {
		bullets = append(bullets, fmt.Sprintf(
			"API routes changed (approximate +%d/-%d) — verify route registration and handler wiring.", added, removed))
	}
	if dirty {
		bullets = append(bullets, "Working tree has uncommitted changes — this digest includes them.")
	}
	if len(bullets) == 0 {
		bullets = append(bullets, "No schema, route, or package signals beyond the file list — changes look localized.")
	}
	if len(bullets) < 3 {
		bullets = append(bullets, "See the diffstat and changed-areas sections above for full scope.")
	}
	if len(bullets) > 6 {
		bullets = bullets[:6]
	}
	b.WriteString("## What to review\n\n")
	for _, bullet := range bullets {
		fmt.Fprintf(&b, "- %s\n", bullet)
	}

	return b.String(), nil
}

// areaOf groups a changed file's path the same way Changes' changedByModule
// does: services/<x> or apps/<x> as a two-segment area, otherwise just the
// first path segment.
func areaOf(path string) string {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) >= 2 && (parts[0] == "services" || parts[0] == "apps") {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// areasWithPrefix returns the subset of areaOrder (already count-sorted)
// whose name starts with prefix, plus their combined file count.
func areasWithPrefix(areaOrder []string, areaCounts map[string]int, prefix string) ([]string, int) {
	var areas []string
	total := 0
	for _, a := range areaOrder {
		if strings.HasPrefix(a, prefix) {
			areas = append(areas, a)
			total += areaCounts[a]
		}
	}
	return areas, total
}

// routeDelta runs `git diff mergeBase -- file` and counts added/removed
// lines that look like a Fiber route-registration call (see routeVerbRe) —
// the line-level approximation documented on WhatChanged's "API route
// delta" section.
func routeDelta(repoDir, mergeBase, file string) (added, removed int) {
	out, err := gitOutput(repoDir, "diff", "--no-ext-diff", mergeBase, "--", file)
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+") && routeVerbRe.MatchString(line):
			added++
		case strings.HasPrefix(line, "-") && routeVerbRe.MatchString(line):
			removed++
		}
	}
	return added, removed
}
