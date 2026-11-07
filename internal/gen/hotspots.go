package gen

import (
	"bufio"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// FileStat is one file's churn in Hotspots' report: how many distinct
// commits touched it (Churn/Commits — the "hotspot" ranking metric) and the
// total added+deleted line count across those commits.
type FileStat struct {
	Path    string `json:"path"`
	Commits int    `json:"commits"` // number of distinct commits touching this file
	Churn   int    `json:"churn"`   // == Commits; the ranking metric
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

// Hotspots aggregates `git log --numstat` over repoDir: for each file, how
// many distinct commits touched it and the total lines added/deleted. If
// sinceRef is non-empty, only commits in sinceRef..HEAD are considered;
// otherwise the whole history of HEAD is used. Results are sorted by churn
// (commit count) descending and capped at top.
func Hotspots(repoDir, sinceRef string, top int) ([]FileStat, error) {
	args := []string{"log", "--no-merges", "--numstat", "--pretty=format:@@%H"}
	if sinceRef != "" {
		args = append(args, sinceRef+"..HEAD")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git log --numstat in %s: %w\n%s", repoDir, err, ee.Stderr)
		}
		return nil, fmt.Errorf("git log --numstat in %s: %w", repoDir, err)
	}

	type agg struct {
		commits int
		added   int
		deleted int
	}
	stats := map[string]*agg{}
	var order []string

	seenInCommit := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "@@") {
			seenInCommit = map[string]bool{}
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		added := parseNumstatField(fields[0])
		deleted := parseNumstatField(fields[1])
		path := fields[2]

		a, ok := stats[path]
		if !ok {
			a = &agg{}
			stats[path] = a
			order = append(order, path)
		}
		if !seenInCommit[path] {
			a.commits++
			seenInCommit[path] = true
		}
		a.added += added
		a.deleted += deleted
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan git log output: %w", err)
	}

	result := make([]FileStat, 0, len(order))
	for _, path := range order {
		a := stats[path]
		result = append(result, FileStat{
			Path:    path,
			Commits: a.commits,
			Churn:   a.commits,
			Added:   a.added,
			Deleted: a.deleted,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Churn != result[j].Churn {
			return result[i].Churn > result[j].Churn
		}
		return result[i].Path < result[j].Path
	})
	if top > 0 && len(result) > top {
		result = result[:top]
	}
	return result, nil
}

// parseNumstatField parses one of git numstat's added/deleted columns,
// which is "-" for binary files.
func parseNumstatField(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
