package gen

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// contextPackStructureSkipDirs are directories the "Structure" section's
// directory tree never descends into — VCS/build/dependency noise that
// would blow the token budget without adding signal.
var contextPackStructureSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".next":        true,
	"dist":         true,
	"obs-data":     true,
}

// ContextPack assembles a compact, paste-ready Markdown brief of the repo
// at repoDir, analyzing the Go module at moduleDir. base is the branch
// used for any "recent activity" framing. It is token-budgeted: each
// section is capped so the whole brief stays small enough to paste into
// an LLM prompt.
//
// Every section is best-effort: a sub-generator erroring (no migrations,
// no Fiber routes, no git history, …) only skips that section with a short
// note — it never fails the whole brief.
func ContextPack(repoDir, moduleDir, base string) (string, error) {
	var b strings.Builder

	repoName := filepath.Base(filepath.Clean(repoDir))
	fmt.Fprintf(&b, "# %s — AI context pack\n\n", repoName)
	fmt.Fprintf(&b, "_Generated %s (base: `%s`) — paste this into an AI assistant to give it accurate project context in one shot._\n\n",
		time.Now().UTC().Format(time.RFC3339), base)

	writeContextPackOverview(&b, repoDir, moduleDir)
	writeContextPackStructure(&b, repoDir)
	writeContextPackArchitecture(&b, repoDir, moduleDir)
	writeContextPackDataModel(&b, moduleDir)
	writeContextPackAPISurface(&b, repoDir, moduleDir)
	writeContextPackHotspots(&b, repoDir)
	writeContextPackNav(&b, repoDir, moduleDir)

	return b.String(), nil
}

// writeContextPackOverview writes the "Overview" section: repo name,
// primary Go module path, detected languages, and the README's first
// paragraph (capped).
func writeContextPackOverview(b *strings.Builder, repoDir, moduleDir string) {
	b.WriteString("## Overview\n\n")

	modPath, err := readModulePath(moduleDir)
	if err != nil {
		fmt.Fprintf(b, "- Go module: _unavailable (%v)_\n", err)
	} else {
		fmt.Fprintf(b, "- Primary Go module: `%s`\n", modPath)
	}

	langs := []string{"Go"}
	if hasContextPackFrontend(repoDir) {
		langs = append(langs, "TypeScript/JavaScript (frontend)")
	}
	fmt.Fprintf(b, "- Languages: %s\n", strings.Join(langs, ", "))

	if readme := firstReadmeParagraph(repoDir); readme != "" {
		fmt.Fprintf(b, "\n%s\n", readme)
	}
	b.WriteString("\n")
}

// hasContextPackFrontend reports whether repoDir looks like it also hosts a
// frontend: a package.json at the repo root, or under any apps/* directory.
func hasContextPackFrontend(repoDir string) bool {
	if _, err := os.Stat(filepath.Join(repoDir, "package.json")); err == nil {
		return true
	}
	matches, _ := filepath.Glob(filepath.Join(repoDir, "apps", "*", "package.json"))
	return len(matches) > 0
}

// firstReadmeParagraph returns the first prose paragraph of repoDir's
// README (skipping headings and badge lines), capped to ~400 characters.
// Returns "" if no README is found.
func firstReadmeParagraph(repoDir string) string {
	var content []byte
	for _, name := range []string{"README.md", "Readme.md", "readme.md", "README"} {
		if b, err := os.ReadFile(filepath.Join(repoDir, name)); err == nil {
			content = b
			break
		}
	}
	if content == nil {
		return ""
	}

	var para []string
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			if len(para) > 0 {
				break
			}
			continue // heading(s) before the first paragraph
		}
		if strings.HasPrefix(line, "![") || strings.HasPrefix(line, "[![") {
			continue // badges
		}
		para = append(para, line)
	}

	text := strings.Join(para, " ")
	const maxChars = 400
	if len(text) > maxChars {
		text = strings.TrimSpace(text[:maxChars]) + "…"
	}
	return text
}

// writeContextPackStructure writes the "Structure" section: a compact
// top-2-levels directory tree of repoDir, capped to ~40 lines.
func writeContextPackStructure(b *strings.Builder, repoDir string) {
	b.WriteString("## Structure\n\n```\n")
	lines := contextPackDirTree(repoDir, 2, 40)
	if len(lines) == 0 {
		b.WriteString("(unavailable)\n")
	} else {
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("```\n\n")
}

// contextPackDirTree renders a depth-limited, alphabetically sorted
// directory listing of root, skipping contextPackStructureSkipDirs and any
// dot-directory, capped at capLines total lines (a final "…" line marks
// truncation).
func contextPackDirTree(root string, maxDepth, capLines int) []string {
	var lines []string
	var walk func(dir, prefix string, depth int) bool // false => caller should stop entirely (cap hit)
	walk = func(dir, prefix string, depth int) bool {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return true
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if contextPackStructureSkipDirs[e.Name()] || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			if len(lines) >= capLines {
				lines = append(lines, prefix+"…")
				return false
			}
			full := filepath.Join(dir, name)
			isDir := false
			if info, err := os.Stat(full); err == nil && info.IsDir() {
				isDir = true
			}
			label := name
			if isDir {
				label += "/"
			}
			lines = append(lines, prefix+label)
			if isDir && depth < maxDepth {
				if !walk(full, prefix+"  ", depth+1) {
					return false
				}
			}
		}
		return true
	}
	walk(root, "", 1)
	if len(lines) > capLines {
		lines = lines[:capLines]
	}
	return lines
}

// writeContextPackArchitecture writes the "Architecture (Go packages)"
// section from GraphModel: packages grouped by kind, each with its
// description and out-degree, capped to the top ~40 by (dependents +
// imports), plus a top-8 "hubs" list by dependents.
func writeContextPackArchitecture(b *strings.Builder, repoDir, moduleDir string) {
	b.WriteString("## Architecture (Go packages)\n\n")

	g, err := GraphModel(repoDir, moduleDir)
	if err != nil {
		fmt.Fprintf(b, "_skipped: %v_\n\n", err)
		return
	}
	if len(g.Nodes) == 0 {
		b.WriteString("_no packages found_\n\n")
		return
	}

	const maxPkgs = 40
	ranked := append([]GraphNode{}, g.Nodes...)
	sort.Slice(ranked, func(i, j int) bool {
		si := ranked[i].Metrics.Dependents + ranked[i].Metrics.Imports
		sj := ranked[j].Metrics.Dependents + ranked[j].Metrics.Imports
		if si != sj {
			return si > sj
		}
		return ranked[i].ID < ranked[j].ID
	})
	truncated := 0
	shown := ranked
	if len(shown) > maxPkgs {
		truncated = len(shown) - maxPkgs
		shown = shown[:maxPkgs]
	}

	byKind := map[string][]GraphNode{}
	var kinds []string
	for _, n := range shown {
		if _, ok := byKind[n.Kind]; !ok {
			kinds = append(kinds, n.Kind)
		}
		byKind[n.Kind] = append(byKind[n.Kind], n)
	}
	sort.Strings(kinds)

	for _, kind := range kinds {
		list := byKind[kind]
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		fmt.Fprintf(b, "**%s** (%d)\n\n", kind, len(list))
		for _, n := range list {
			desc := ""
			if n.Description.Text != "" {
				desc = " — " + contextPackOneLine(n.Description.Text, 160)
			}
			fmt.Fprintf(b, "- `%s`%s (deps: %d)\n", n.ID, desc, n.Metrics.Imports)
		}
		b.WriteString("\n")
	}
	if truncated > 0 {
		fmt.Fprintf(b, "…and %d more package(s)\n\n", truncated)
	}

	const maxHubs = 8
	hubs := append([]GraphNode{}, g.Nodes...)
	sort.Slice(hubs, func(i, j int) bool {
		if hubs[i].Metrics.Dependents != hubs[j].Metrics.Dependents {
			return hubs[i].Metrics.Dependents > hubs[j].Metrics.Dependents
		}
		return hubs[i].ID < hubs[j].ID
	})
	if len(hubs) > maxHubs {
		hubs = hubs[:maxHubs]
	}
	if len(hubs) > 0 && hubs[0].Metrics.Dependents > 0 {
		b.WriteString("**Most depended-on packages (hubs):**\n\n")
		for i, n := range hubs {
			if n.Metrics.Dependents == 0 {
				break
			}
			fmt.Fprintf(b, "%d. `%s` (dependents: %d)\n", i+1, n.ID, n.Metrics.Dependents)
		}
		b.WriteString("\n")
	}
}

// contextPackMigrationsDir walks moduleDir's subtree for the first
// directory whose name contains "migration" and holds at least one *.sql
// file — a package-local stand-in for plugins.findMigrationsDirs (this
// package cannot import internal/plugins, which itself imports nothing
// from gen, to avoid an import cycle risk and keep gen dependency-free).
func contextPackMigrationsDir(moduleDir string) (string, bool) {
	var found string
	_ = filepath.WalkDir(moduleDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort walk: skip, don't abort
		}
		if found != "" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != moduleDir && (name == "vendor" || name == "node_modules" || name == ".git" || strings.HasPrefix(name, ".")) {
			return filepath.SkipDir
		}
		if !strings.Contains(strings.ToLower(name), "migration") {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil
		}
		for _, e := range entries {
			n := strings.ToLower(e.Name())
			if !e.IsDir() && strings.HasSuffix(n, ".sql") {
				found = path
				return filepath.SkipDir
			}
		}
		return nil
	})
	return found, found != ""
}

// writeContextPackDataModel writes the "Data model" section from
// ERGraphModel against the first migrations directory found under
// moduleDir, capped to ~40 tables.
func writeContextPackDataModel(b *strings.Builder, moduleDir string) {
	b.WriteString("## Data model\n\n")

	dir, ok := contextPackMigrationsDir(moduleDir)
	if !ok {
		b.WriteString("_skipped: no migrations directory found_\n\n")
		return
	}
	g, err := ERGraphModel(dir, dir)
	if err != nil {
		fmt.Fprintf(b, "_skipped: %v_\n\n", err)
		return
	}
	if len(g.Nodes) == 0 {
		b.WriteString("_no tables found_\n\n")
		return
	}

	nodes := append([]GraphNode{}, g.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	const maxTables = 40
	truncated := 0
	if len(nodes) > maxTables {
		truncated = len(nodes) - maxTables
		nodes = nodes[:maxTables]
	}
	for _, n := range nodes {
		pk, fk := 0, 0
		if n.Table != nil {
			for _, c := range n.Table.Columns {
				if c.PK {
					pk++
				}
				if c.FK {
					fk++
				}
			}
		}
		fmt.Fprintf(b, "- `%s` — %d column(s)", n.ID, n.Metrics.Columns)
		if pk > 0 || fk > 0 {
			fmt.Fprintf(b, " (PK: %d, FK: %d)", pk, fk)
		}
		b.WriteString("\n")
	}
	if truncated > 0 {
		fmt.Fprintf(b, "\n…and %d more table(s)\n", truncated)
	}
	b.WriteString("\n")
}

// writeContextPackAPISurface writes the "API surface" section from
// APIRoutes, grouped as a flat METHOD /path -> Handler list, capped to
// ~60 routes.
func writeContextPackAPISurface(b *strings.Builder, repoDir, moduleDir string) {
	b.WriteString("## API surface\n\n")

	routes, err := APIRoutes(repoDir, moduleDir)
	if err != nil {
		fmt.Fprintf(b, "_skipped: %v_\n\n", err)
		return
	}
	if len(routes) == 0 {
		b.WriteString("_no routes detected_\n\n")
		return
	}

	const maxRoutes = 60
	total := len(routes)
	shown := routes
	if len(shown) > maxRoutes {
		shown = shown[:maxRoutes]
	}
	for _, r := range shown {
		fmt.Fprintf(b, "- `%s %s` → %s\n", r.Method, r.Path, r.Handler)
	}
	if total > maxRoutes {
		fmt.Fprintf(b, "\n…truncated (%d total routes)\n", total)
	}
	b.WriteString("\n")
}

// writeContextPackHotspots writes the "Hotspots" section: the top 10
// highest-churn files from Hotspots.
func writeContextPackHotspots(b *strings.Builder, repoDir string) {
	b.WriteString("## Hotspots (highest churn)\n\n")

	stats, err := Hotspots(repoDir, "", 10)
	if err != nil {
		fmt.Fprintf(b, "_skipped: %v_\n\n", err)
		return
	}
	if len(stats) == 0 {
		b.WriteString("_no git history available_\n\n")
		return
	}
	for _, s := range stats {
		fmt.Fprintf(b, "- `%s` — %d commit(s) (+%d/-%d)\n", s.Path, s.Commits, s.Added, s.Deleted)
	}
	b.WriteString("\n")
}

// writeContextPackNav writes the "How to navigate" footer: the module
// root, entrypoint/application packages (from GraphModel, best-effort),
// and the migrations dir (if any).
func writeContextPackNav(b *strings.Builder, repoDir, moduleDir string) {
	b.WriteString("## How to navigate\n\n")

	relMod, err := filepath.Rel(repoDir, moduleDir)
	if err != nil || relMod == "." {
		relMod = "repo root"
	} else {
		relMod = "`" + filepath.ToSlash(relMod) + "`"
	}
	fmt.Fprintf(b, "- Go module root: %s\n", relMod)

	if g, err := GraphModel(repoDir, moduleDir); err == nil {
		var entries, apps []string
		for _, n := range g.Nodes {
			switch n.Kind {
			case "entry":
				entries = append(entries, n.ID)
			case "service":
				apps = append(apps, n.ID)
			}
		}
		sort.Strings(entries)
		sort.Strings(apps)
		if len(entries) > 0 {
			fmt.Fprintf(b, "- Entrypoint package(s): %s\n", contextPackJoinTicked(entries))
		}
		if len(apps) > 0 {
			if len(apps) > 10 {
				apps = apps[:10]
			}
			fmt.Fprintf(b, "- Main application packages: %s\n", contextPackJoinTicked(apps))
		}
	}

	if dir, ok := contextPackMigrationsDir(moduleDir); ok {
		rel := dir
		if r, err := filepath.Rel(repoDir, dir); err == nil {
			rel = r
		}
		fmt.Fprintf(b, "- Migrations: `%s`\n", filepath.ToSlash(rel))
	}

	b.WriteString("- This brief is generated by `observer gen contextpack` — re-run it anytime for a fresh snapshot.\n")
}

// contextPackOneLine collapses a (possibly multi-line) description into a
// single line — bullets must stay one-per-line for the brief to paste
// cleanly — and caps it to maxChars.
func contextPackOneLine(s string, maxChars int) string {
	fields := strings.Fields(s)
	line := strings.Join(fields, " ")
	if len(line) > maxChars {
		line = strings.TrimSpace(line[:maxChars]) + "…"
	}
	return line
}

func contextPackJoinTicked(ids []string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "`" + id + "`"
	}
	return strings.Join(parts, ", ")
}
