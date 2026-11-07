package gen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/taikuri-infra/observer/internal/gitmeta"
	"github.com/taikuri-infra/observer/internal/runner"
)

// overviewIndex mirrors obs-data/_index.json's schema ({"tabs": {name:
// Artifact}}) — see internal/server's private `index` type, R7.
type overviewIndex struct {
	Tabs map[string]runner.Artifact `json:"tabs"`
}

// Overview renders a markdown landing page: the repo's branch/HEAD/dirty
// state, plus a freshness table of every tab recorded in
// obs-data/_index.json. Tolerates a missing _index.json (e.g. before any
// tab has ever been run).
func Overview(repo, base string) (string, error) {
	meta, err := gitmeta.Collect(repo, base)
	if err != nil {
		return "", fmt.Errorf("collect git meta: %w", err)
	}

	idx, err := readIndex(repo)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("# Overview\n\n")
	b.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Branch | `%s` |\n", meta.Branch)
	fmt.Fprintf(&b, "| HEAD | `%s` |\n", shortSHA(meta.Head))
	fmt.Fprintf(&b, "| Working tree | %s |\n\n", dirtyLabel(meta.Dirty))

	if len(idx.Tabs) == 0 {
		b.WriteString("No tabs have been generated yet — click **Update** on a tab to populate this page.\n")
		return b.String(), nil
	}

	names := make([]string, 0, len(idx.Tabs))
	for name := range idx.Tabs {
		names = append(names, name)
	}
	sort.Strings(names)

	b.WriteString("## Tabs\n\n")
	b.WriteString("| Tab | Renderer | Generated at | Commit | Status |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, name := range names {
		a := idx.Tabs[name]
		status := "ok"
		if a.Err != "" {
			status = "error"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | `%s` | %s |\n", name, a.Renderer, a.GeneratedAt, shortSHA(a.Head), status)
	}
	return b.String(), nil
}

func readIndex(repo string) (overviewIndex, error) {
	idx := overviewIndex{Tabs: map[string]runner.Artifact{}}
	b, err := os.ReadFile(filepath.Join(repo, "obs-data", "_index.json"))
	if os.IsNotExist(err) {
		return idx, nil
	}
	if err != nil {
		return idx, fmt.Errorf("read obs-data/_index.json: %w", err)
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return idx, fmt.Errorf("parse obs-data/_index.json: %w", err)
	}
	if idx.Tabs == nil {
		idx.Tabs = map[string]runner.Artifact{}
	}
	return idx, nil
}

func shortSHA(sha string) string {
	if len(sha) > 9 {
		return sha[:9]
	}
	return sha
}
