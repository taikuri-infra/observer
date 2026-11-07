// Package plugins builds observer's built-in tab set by detecting a repo's
// structure: nested Go modules, frontend packages, and SQL migrations
// directories.
package plugins

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skipDirs are directories detection never descends into — dependency
// caches and build output that may contain their own (generated,
// irrelevant) go.mod/package.json files.
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	".git":         true,
	"dist":         true,
	"build":        true,
	"coverage":     true,
}

// isHidden reports whether name is a dot-directory (.next, .turbo, .cache,
// …) — always build/tool output, never a module worth detecting.
func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

// findModuleDirs walks repo (skipping skipDirs), only descending up to
// maxDepth levels, and returns every directory — relative to repo, "/"
// separated, sorted — that directly contains a file named marker (e.g.
// "go.mod" or "package.json"). The repo root itself is never considered
// "nested", so a marker file there is excluded.
func findModuleDirs(repo, marker string, maxDepth int) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(repo, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			if rel == "." {
				return nil
			}
			if skipDirs[d.Name()] || isHidden(d.Name()) {
				return filepath.SkipDir
			}
			if len(strings.Split(rel, "/")) > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}

		if d.Name() != marker {
			return nil
		}
		dir := filepath.Dir(rel)
		if dir == "." {
			return nil // marker at the repo root isn't "nested"
		}
		dirs = append(dirs, dir)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(dirs)
	return dirs, nil
}

// findMigrationsDirs walks moduleDir's whole subtree (skipping skipDirs,
// unbounded depth — migrations directories are often nested deep inside a
// hexagonal-architecture adapter layer) and returns every directory —
// relative to moduleDir, "/" separated, sorted — whose name contains
// "migration" and that directly contains at least one non-"*_down.sql"
// *.sql file.
func findMigrationsDirs(moduleDir string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(moduleDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(moduleDir, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel != "." && (skipDirs[d.Name()] || isHidden(d.Name())) {
			return filepath.SkipDir
		}
		if !strings.Contains(strings.ToLower(d.Name()), "migration") {
			return nil
		}
		if hasUpSQL(path) {
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(found)
	return found, nil
}

// hasUpSQL reports whether dir directly contains at least one *.sql file
// that isn't a "*_down.sql" teardown migration.
func hasUpSQL(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".sql") && !strings.Contains(name, "_down") {
			return true
		}
	}
	return false
}

// upPath returns the "../" prefix needed to get from dir (relative to the
// repo root) back to the repo root — e.g. "services/api" -> "../../". An
// empty/"." dir (the repo root itself) needs no prefix.
func upPath(dir string) string {
	if dir == "" || dir == "." {
		return ""
	}
	n := len(strings.Split(filepath.ToSlash(dir), "/"))
	return strings.Repeat("../", n)
}

// safeName turns a relative path into a filename-safe stem, e.g.
// "services/api" -> "services-api".
func safeName(dir string) string {
	return strings.ReplaceAll(filepath.ToSlash(dir), "/", "-")
}

// outputName disambiguates a shared output filename when more than one tab
// of the same kind is generated (one per detected module): with a single
// module, the name is used as-is; otherwise mod is appended to the stem.
func outputName(name, mod string, count int) string {
	if count <= 1 {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	return stem + "-" + safeName(mod) + ext
}

// tabName disambiguates a tab's display name the same way outputName does
// for its artifact filename.
func tabName(base, mod string, count int) string {
	if count <= 1 {
		return base
	}
	return base + " (" + mod + ")"
}
