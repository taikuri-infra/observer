package gen

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/taikuri-infra/observer/internal/gitmeta"
)

// ChangedGraphModel builds moduleDir's Go-deps graph.json (via GraphModel)
// and annotates each node with Changed/ChangedFiles: whether any file under
// that package's directory differs between HEAD and the merge-base with
// base, and which files those are. It's the data behind the "Changes" tab's
// highlighted service map — the same underlying comparison Changes() renders
// as a markdown diffstat, here grouped to package-directory granularity so
// the map renderer can light up touched packages instead.
func ChangedGraphModel(repoDir, moduleDir, base string) (*Graph, error) {
	g, err := GraphModel(repoDir, moduleDir)
	if err != nil {
		return nil, err
	}
	byDir, err := changedFilesByPackageDir(repoDir, base)
	if err != nil {
		return nil, err
	}
	any := false
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if files := byDir[n.Location.Path]; len(files) > 0 {
			n.Changed = true
			n.ChangedFiles = files
			any = true
		}
	}
	g.Meta.Source = "changes"
	g.Meta.Base = base
	g.Meta.AnyChanged = any
	return g, nil
}

// changedFilesByPackageDir maps each repo-relative directory to the sorted
// list of its own changed files: everything `git diff --name-only` reports
// between HEAD and the merge-base with base. A missing merge-base (base not
// found locally, or no common ancestor) degrades to "nothing changed" rather
// than failing the generator — mirroring Changes()'s own tolerance for that
// case.
func changedFilesByPackageDir(repoDir, base string) (map[string][]string, error) {
	meta, err := gitmeta.Collect(repoDir, base)
	if err != nil {
		return nil, fmt.Errorf("collect git meta: %w", err)
	}
	byDir := map[string][]string{}
	if meta.MergeBase == "" {
		return byDir, nil
	}
	out, err := gitmeta.Command(repoDir, "diff", "--name-only", meta.MergeBase).Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only %s: %w", meta.MergeBase, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(filepath.ToSlash(line)))
		byDir[dir] = append(byDir[dir], line)
	}
	return byDir, nil
}
