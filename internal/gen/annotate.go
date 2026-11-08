package gen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PackageDocResult is what one package directory's doc comments yield: the
// godoc text (source for description.text when no //observer:summary
// exists), any //observer: annotation values found across the package's
// files, and which file the godoc text was extracted from (for
// location.primaryFile).
type PackageDocResult struct {
	PrimaryFile string // base filename the godoc came from (or, if no file had a doc comment, the first non-test .go file — always set when goFiles is non-empty)
	GodocText   string // package doc, directives/banners/"Package <name> " prefix stripped; "" if no godoc comment
	Summary     string // //observer:summary value; "" if absent
	Owner       string // //observer:owner value; "" if absent
	Task        string // //observer:task value; "" if absent
}

// generatedBannerRe matches the conventional "Code generated … DO NOT
// EDIT." banner (comment markers already removed by CommentGroup.Text()).
// Text() keeps it because it is prose, not a //tool:directive — so we strip
// it ourselves to keep machine banners out of a node's description.
var generatedBannerRe = regexp.MustCompile(`(?m)^[ \t]*Code generated .* DO NOT EDIT\.[ \t]*$`)

// jiraIDRe matches a Jira-shaped work-item id: one or more uppercase
// letters, a dash, and digits (e.g. "OBS-42"). "SP-…" is excluded (kept
// "serviceplus" by default) since ServicePlus' own convention uses that
// prefix for its native issue ids too (keeping the rule simple, per the
// design spec's "keep simple; document your choice").
var jiraIDRe = regexp.MustCompile(`^[A-Z]+-[0-9]+$`)

// InferLinkType returns the links[] "type" for an //observer:task value:
// "serviceplus" by default, or "jira" when id looks like a Jira key (the
// `[A-Z]+-\d+` convention) with a prefix other than "SP".
func InferLinkType(task string) string {
	if jiraIDRe.MatchString(task) && !strings.HasPrefix(task, "SP-") {
		return "jira"
	}
	return "serviceplus"
}

// ExtractPackageDoc scans dir's non-test .go files (in sorted/deterministic
// order) and returns the package's description inputs:
//
//   - GodocText: the prose of the first file (sorted) carrying a package
//     doc comment — the comment group immediately above `package X`, which
//     go/parser attaches to File.Doc. We use CommentGroup.Text(), so block
//     (`/* … */`) comments work and //tool:directive lines (//go:generate,
//     //observer:…) plus a "Code generated … DO NOT EDIT." banner are
//     stripped from the prose.
//   - Summary/Owner/Task: //observer:<key> <value> directives, merged across
//     ALL of the package's files (first file, in sorted order, to carry a
//     given key wins). A package may legitimately keep its godoc in doc.go
//     and its //observer:summary above `package` in another file; precedence
//     (summary > godoc) must still hold across that split, so directives are
//     never tied to the single file the godoc came from.
func ExtractPackageDoc(dir string, goFiles []string) (PackageDocResult, error) {
	names := filterNonTestGo(goFiles)
	sort.Strings(names)

	var res PackageDocResult
	if len(names) > 0 {
		res.PrimaryFile = names[0] // fallback until a godoc'd file is found
	}

	fset := token.NewFileSet()
	godocFound := false
	for _, name := range names {
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.PackageClauseOnly)
		if err != nil {
			continue // unparsable file: skip rather than fail the whole generator
		}
		if f.Doc == nil || len(f.Doc.List) == 0 {
			continue
		}
		// //observer: directives: merge across every file (first wins).
		mergeAnnotations(f.Doc.List, &res)
		// godoc prose: take the first file (sorted) that actually has some.
		if !godocFound {
			if text := cleanDoc(f.Doc.Text(), f.Name.Name); text != "" {
				res.GodocText = text
				res.PrimaryFile = name
				godocFound = true
			}
		}
	}
	return res, nil
}

// mergeAnnotations pulls //observer:<key> <value> directives out of a doc
// comment group's raw lines and stores them on res without overwriting a
// value an earlier (sorted-earlier) file already set.
func mergeAnnotations(list []*ast.Comment, res *PackageDocResult) {
	for _, c := range list {
		raw := strings.TrimPrefix(c.Text, "//")
		if raw == c.Text || !strings.HasPrefix(raw, "observer:") {
			continue // block comment, or not an //observer: directive
		}
		applyAnnotation(strings.TrimPrefix(raw, "observer:"), res)
	}
}

// applyAnnotation parses one //observer: line's "<key> <value>" body
// (rest, with the "observer:" prefix already stripped) and stores it on
// res — first value wins, so a later file can't clobber an earlier one.
func applyAnnotation(rest string, res *PackageDocResult) {
	parts := strings.SplitN(strings.TrimSpace(rest), " ", 2)
	if len(parts) == 0 || parts[0] == "" {
		return
	}
	key := parts[0]
	value := ""
	if len(parts) == 2 {
		value = strings.TrimSpace(parts[1])
	}
	switch key {
	case "summary":
		if res.Summary == "" {
			res.Summary = value
		}
	case "owner":
		if res.Owner == "" {
			res.Owner = value
		}
	case "task":
		if res.Task == "" {
			res.Task = value
		}
	}
}

// cleanDoc normalizes CommentGroup.Text() output into a node description:
// strips a leftover "Code generated … DO NOT EDIT." banner (Text() keeps it
// since it's not a directive) and the conventional "Package <pkgName> "
// prefix, so the text reads as a sentence fragment (matching the style used
// across this repo, e.g. "implements the tenant provisioning pipeline…").
func cleanDoc(text, pkgName string) string {
	text = generatedBannerRe.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	prefix := "Package " + pkgName + " "
	if strings.HasPrefix(text, prefix) {
		text = strings.TrimPrefix(text, prefix)
	}
	return strings.TrimSpace(text)
}

// filterNonTestGo drops "_test.go" files — a package's doc comment is never
// written there, and we don't want test-file godoc leaking into node
// descriptions.
func filterNonTestGo(files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		out = append(out, f)
	}
	return out
}
