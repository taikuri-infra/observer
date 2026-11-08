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

// PackageDocResult is what one package directory's doc comment yields:
// the godoc text (source for description.text when no //observer:summary
// exists), any //observer: annotation values found in that same comment
// block, and which file it was extracted from (for location.primaryFile).
type PackageDocResult struct {
	PrimaryFile string // base filename the doc/annotations came from (or, if none had a doc comment, the first non-test .go file — always set when goFiles is non-empty)
	GodocText   string // package doc, "Package <name> " prefix stripped; "" if no godoc comment
	Summary     string // //observer:summary value; "" if absent
	Owner       string // //observer:owner value; "" if absent
	Task        string // //observer:task value; "" if absent
}

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
// order) for the first one carrying a package doc comment — the `//` block
// immediately above `package X`, which go/parser attaches to File.Doc only
// when it directly precedes the package clause with no intervening blank
// line — and parses both the plain godoc text and any //observer:<key>
// <value> directive lines out of that same comment block.
//
// //observer: lines use Go's directive-comment convention (no space between
// "//" and the key, e.g. "//observer:summary …"), so they never show up as
// prose in the godoc text and godoc readers already skip them.
func ExtractPackageDoc(dir string, goFiles []string) (PackageDocResult, error) {
	names := filterNonTestGo(goFiles)
	sort.Strings(names)

	var fallback string
	if len(names) > 0 {
		fallback = names[0]
	}

	fset := token.NewFileSet()
	for _, name := range names {
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.PackageClauseOnly)
		if err != nil {
			continue // unparsable file: skip rather than fail the whole generator
		}
		if f.Doc == nil || len(f.Doc.List) == 0 {
			continue
		}
		res := parseDocComment(f.Doc.List, f.Name.Name)
		res.PrimaryFile = name
		return res, nil
	}
	return PackageDocResult{PrimaryFile: fallback}, nil
}

// parseDocComment walks a package doc comment's raw lines, splitting them
// into godoc prose and //observer: directive values, then returns the
// prose joined into GodocText with the "Package <pkgName> " prefix (Go's
// standard package-doc convention) stripped.
func parseDocComment(list []*ast.Comment, pkgName string) PackageDocResult {
	var res PackageDocResult
	var docLines []string
	for _, c := range list {
		raw := strings.TrimPrefix(c.Text, "//")
		if strings.HasPrefix(raw, "observer:") {
			applyAnnotation(strings.TrimPrefix(raw, "observer:"), &res)
			continue
		}
		docLines = append(docLines, strings.TrimPrefix(raw, " "))
	}
	res.GodocText = joinDocLines(docLines, pkgName)
	return res
}

// applyAnnotation parses one //observer: line's "<key> <value>" body
// (rest, with the "observer:" prefix already stripped) and stores it on res.
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
		res.Summary = value
	case "owner":
		res.Owner = value
	case "task":
		res.Task = value
	}
}

// joinDocLines joins a package comment's prose lines into one string,
// treating blank comment lines ("//") as paragraph breaks, then strips the
// conventional "Package <pkgName> " prefix so description.text reads as a
// sentence fragment (matching the style already used across this repo's
// package comments, e.g. "implements the tenant provisioning pipeline…").
func joinDocLines(lines []string, pkgName string) string {
	var paras []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			paras = append(paras, strings.Join(cur, " "))
			cur = nil
		}
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		cur = append(cur, l)
	}
	flush()
	text := strings.Join(paras, "\n\n")
	prefix := "Package " + pkgName + " "
	if strings.HasPrefix(text, prefix) {
		text = strings.TrimPrefix(text, prefix)
	}
	return text
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
