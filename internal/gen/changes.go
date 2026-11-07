// Package gen holds observer's native (zero-external-dependency) generators:
// small Go analyses that don't need a bundled tool (changes-since-base,
// SQL-migrations-to-DBML, go deps, hotspots, …).
package gen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/taikuri-infra/observer/internal/gitmeta"
)

// Changes renders a markdown report of what changed in dir's working tree +
// HEAD versus the merge-base with base (e.g. "main"). Ported from
// servicePlus/scripts/refresh-architecture.sh's changes_since_main.
func Changes(dir, base string) (string, error) {
	meta, err := gitmeta.Collect(dir, base)
	if err != nil {
		return "", fmt.Errorf("collect git meta: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Changes since `%s`\n\n", base)
	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Branch | `%s` |\n", meta.Branch)

	if meta.MergeBase == "" {
		fmt.Fprintf(&b, "| Compared against | `%s` (no merge-base found) |\n", base)
		fmt.Fprintf(&b, "| Working tree | %s |\n\n", dirtyLabel(meta.Dirty))
		fmt.Fprintf(&b, "> No merge-base with `%s` — this branch has no common ancestor with it "+
			"(or `%s` doesn't exist locally). Showing the full commit history instead.\n\n", base, base)

		log, _ := gitOutput(dir, "log", "--oneline", "-40")
		fmt.Fprintf(&b, "## Commit history\n\n```\n%s\n```\n", orNone(log))
		return b.String(), nil
	}

	shortBase := meta.MergeBase
	if len(shortBase) > 9 {
		shortBase = shortBase[:9]
	}
	fmt.Fprintf(&b, "| Compared against | `%s` (merge-base `%s`) |\n", base, shortBase)
	fmt.Fprintf(&b, "| Working tree | %s |\n\n", dirtyLabel(meta.Dirty))

	changed, _ := gitOutput(dir, "diff", "--name-only", meta.MergeBase)
	diffstat, _ := gitOutput(dir, "diff", "--stat", meta.MergeBase)

	fmt.Fprintf(&b, "## Changed files by module\n\n```\n%s\n```\n\n", orNone(changedByModule(changed)))
	fmt.Fprintf(&b, "## Routes / handlers touched\n\n```\n%s\n```\n\n", orNone(grepLines(changed, "router", "/handler/")))
	fmt.Fprintf(&b, "## Schema objects (migrations) touched\n\n```\n%s\n```\n\n", orNone(grepLines(changed, "migrations/")))
	fmt.Fprintf(&b, "## Diffstat\n\n```\n%s\n```\n", orNone(tailLines(diffstat, 40)))

	return b.String(), nil
}

func dirtyLabel(dirty bool) string {
	if dirty {
		return "uncommitted changes present (not included in this diff)"
	}
	return "clean"
}

// gitOutput runs git through gitmeta.Command (hardened against malicious
// repo-local config/hooks — the repos this generator inspects may be
// untrusted).
func gitOutput(dir string, args ...string) (string, error) {
	out, err := gitmeta.Command(dir, args...).Output()
	return strings.TrimSpace(string(out)), err
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// grepLines keeps only lines containing any of substrs.
func grepLines(s string, substrs ...string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		for _, sub := range substrs {
			if strings.Contains(line, sub) {
				out = append(out, line)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

// changedByModule groups a `git diff --name-only` listing by top module —
// services/<x>, apps/<x>, or the first path segment otherwise — and prints a
// " <count> <module>" line per group, most-changed first (like `sort | uniq -c
// | sort -rn`).
func changedByModule(names string) string {
	counts := map[string]int{}
	var order []string
	for _, line := range strings.Split(names, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "/", 3)
		var mod string
		switch {
		case len(parts) >= 2 && (parts[0] == "services" || parts[0] == "apps"):
			mod = parts[0] + "/" + parts[1]
		default:
			mod = parts[0]
		}
		if counts[mod] == 0 {
			order = append(order, mod)
		}
		counts[mod]++
	}
	// Sort by count descending, stable for ties (insertion order).
	sortByCountDesc(order, counts)
	var b strings.Builder
	for _, mod := range order {
		fmt.Fprintf(&b, "%6s %s\n", strconv.Itoa(counts[mod]), mod)
	}
	return strings.TrimRight(b.String(), "\n")
}

func sortByCountDesc(order []string, counts map[string]int) {
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && counts[order[j]] > counts[order[j-1]]; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
}
