package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// erTable is one CREATE TABLE block.
type erTable struct {
	name    string
	columns []erColumn
}

type erColumn struct {
	name string
	typ  string
	pk   bool
	fk   bool
}

// erRel is one REFERENCES relationship: child.col -> parent.parentCol.
type erRel struct {
	parent    string
	parentCol string
	child     string
	col       string
}

var (
	// createTableRe tolerates an optional schema prefix on the table name
	// (`public.users` -> captures "users", schema stripped) and a closing
	// line that isn't exactly "\n);": indentation before the ")", and
	// trailing table options after it (`) STRICT;`, `) PARTITION BY RANGE
	// (created_at);`) are all accepted — the terminator is "a line whose
	// first non-blank char is ')', up to the ';'".
	createTableRe = regexp.MustCompile(`(?is)CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+(?:[a-zA-Z_][a-zA-Z0-9_]*\.)?([a-zA-Z_][a-zA-Z0-9_]*)\s*\((.*?)\n[ \t]*\)[^;\n]*;`)
	columnRe      = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)\s+([A-Za-z0-9_]+)`)
	// referencesRe strips an optional schema prefix on the referenced table
	// (`REFERENCES public.users(id)` -> parent "users", not "public").
	referencesRe = regexp.MustCompile(`(?i)REFERENCES\s+(?:[a-zA-Z_][a-zA-Z0-9_]*\.)?([a-zA-Z_][a-zA-Z0-9_]*)\s*(?:\(\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\))?`)
	// fkColRe pulls the (first) child column out of a table-level
	// `FOREIGN KEY (a, b) REFERENCES ...` constraint line.
	fkColRe = regexp.MustCompile(`(?i)FOREIGN\s+KEY\s*\(\s*([a-zA-Z_][a-zA-Z0-9_]*)`)
)

// ER parses CREATE TABLE statements out of the *.sql files in migrationsDir
// (skipping anything named "*_down.sql") and renders both a mermaid
// erDiagram and a DBML snapshot. Ported from
// servicePlus/scripts/refresh-architecture.sh's er_diagram python block.
func ER(migrationsDir string) (mermaid string, dbml string, err error) {
	files, err := sqlUpFiles(migrationsDir)
	if err != nil {
		return "", "", err
	}
	var sqlBuf strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", "", fmt.Errorf("read %s: %w", f, err)
		}
		sqlBuf.Write(b)
		sqlBuf.WriteByte('\n')
	}
	tables, rels := parseSQL(sqlBuf.String())
	return renderMermaidER(tables, rels), renderDBML(tables, rels), nil
}

// sqlUpFiles returns the *.sql files directly under dir, sorted, excluding
// any file with "_down" in its name (down-migrations describe teardown, not
// the schema we're documenting).
func sqlUpFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".sql") {
			continue
		}
		if strings.Contains(lower, "_down") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	return files, nil
}

// excludedLinePrefixes are table-level constraint lines, not columns.
var excludedLinePrefixes = []string{"PRIMARY KEY", "UNIQUE", "CONSTRAINT", "FOREIGN KEY", "CHECK"}

func parseSQL(sql string) ([]erTable, []erRel) {
	var tables []erTable
	var rels []erRel
	for _, m := range createTableRe.FindAllStringSubmatch(sql, -1) {
		name := strings.ToLower(m[1])
		body := m[2]
		var cols []erColumn
		for _, raw := range strings.Split(body, "\n") {
			line := strings.TrimSpace(raw)
			line = strings.TrimSuffix(line, ",")
			if line == "" || strings.HasPrefix(line, "--") {
				continue
			}
			upper := strings.ToUpper(line)
			excluded := false
			for _, p := range excludedLinePrefixes {
				if strings.HasPrefix(upper, p) {
					excluded = true
					break
				}
			}
			if excluded {
				// A table-level constraint line is never a column — but a
				// FOREIGN KEY (...) REFERENCES ... line still declares an
				// edge, so scan it for the reference before skipping.
				if rm := referencesRe.FindStringSubmatch(line); rm != nil {
					childCol := "fk"
					if cm := fkColRe.FindStringSubmatch(line); cm != nil {
						childCol = strings.ToLower(cm[1])
					}
					rels = append(rels, newRel(rm, name, childCol))
				}
				continue
			}
			cm := columnRe.FindStringSubmatch(line)
			if cm == nil {
				continue
			}
			col := strings.ToLower(cm[1])
			typ := strings.ToLower(cm[2])
			pk := strings.Contains(upper, "PRIMARY KEY")
			fk := strings.Contains(upper, "REFERENCES")
			cols = append(cols, erColumn{name: col, typ: typ, pk: pk, fk: fk})
			if rm := referencesRe.FindStringSubmatch(line); rm != nil {
				rels = append(rels, newRel(rm, name, col))
			}
		}
		tables = append(tables, erTable{name: name, columns: cols})
	}
	return tables, rels
}

// newRel builds an erRel from a referencesRe match (rm[1]=parent table with
// any schema already stripped, rm[2]=parent column or "") for child.childCol.
// A missing parent column defaults to "id", the overwhelmingly common PK.
func newRel(rm []string, child, childCol string) erRel {
	parentCol := rm[2]
	if parentCol == "" {
		parentCol = "id"
	}
	return erRel{
		parent:    strings.ToLower(rm[1]),
		parentCol: strings.ToLower(parentCol),
		child:     child,
		col:       childCol,
	}
}

// ERGraphModel parses the same CREATE TABLE statements as ER (reusing
// sqlUpFiles/parseSQL) and renders them as a normalized graph.json for the
// canvas "map" renderer instead of mermaid/DBML text: one node per table
// (kind "table", metrics columns/fkOut/fkIn) and one edge per foreign key,
// from the child (the table holding the REFERENCES column) to the parent it
// points at — matching the direction Go-deps edges use ("from" depends on
// "to"). migrationsDir is read from directly (absolute or relative to the
// caller's cwd); locationPath is what every node's location.path is set to
// — normally the repo-relative migrations dir, passed separately since
// ERGraphModel itself never sees the repo root (see cmd/observer's
// runGenER, which resolves migrationsDir but keeps the original --migrations
// value as locationPath).
func ERGraphModel(migrationsDir, locationPath string) (*Graph, error) {
	files, err := sqlUpFiles(migrationsDir)
	if err != nil {
		return nil, err
	}
	var sqlBuf strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		sqlBuf.Write(b)
		sqlBuf.WriteByte('\n')
	}
	tables, rels := parseSQL(sqlBuf.String())

	if locationPath == "" {
		locationPath = migrationsDir
	}
	locationPath = filepath.ToSlash(locationPath)

	fkOut := map[string]int{}
	fkIn := map[string]int{}
	var edges []GraphEdge
	for _, r := range rels {
		fkOut[r.child]++
		fkIn[r.parent]++
		edges = append(edges, GraphEdge{From: r.child, To: r.parent, Weight: 1})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	nodes := make([]GraphNode, 0, len(tables))
	for _, t := range tables {
		cols := make([]TableColumn, 0, len(t.columns))
		for _, c := range t.columns {
			cols = append(cols, TableColumn{Name: c.name, Type: c.typ, PK: c.pk, FK: c.fk})
		}
		nodes = append(nodes, GraphNode{
			ID:    t.name,
			Label: t.name,
			Kind:  "table",
			Metrics: GraphMetrics{
				Columns: len(t.columns),
				FKOut:   fkOut[t.name],
				FKIn:    fkIn[t.name],
			},
			Location:    GraphLocation{Path: locationPath, Files: len(files)},
			Description: GraphDescription{Source: "none"},
			Links:       []GraphLink{},
			Table:       &TableDetail{Columns: cols},
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	return &Graph{
		Meta: GraphMeta{
			Source:        "db-er",
			Module:        locationPath,
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
			SizeBy:        "fkIn",
			ColorBy:       "columns",
			DefaultLayout: "hierarchical",
		},
		Nodes: nodes,
		Edges: edges,
	}, nil
}

func renderMermaidER(tables []erTable, rels []erRel) string {
	var b strings.Builder
	b.WriteString("erDiagram\n")
	for _, t := range tables {
		fmt.Fprintf(&b, "    %s {\n", t.name)
		for _, c := range t.columns {
			tag := ""
			switch {
			case c.pk:
				tag = " PK"
			case c.fk:
				tag = " FK"
			}
			fmt.Fprintf(&b, "        %s %s%s\n", c.typ, c.name, tag)
		}
		b.WriteString("    }\n")
	}
	for _, r := range rels {
		fmt.Fprintf(&b, "    %s ||--o{ %s : \"%s\"\n", r.parent, r.child, r.col)
	}
	return b.String()
}

func renderDBML(tables []erTable, rels []erRel) string {
	var b strings.Builder
	for i, t := range tables {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Table %s {\n", t.name)
		for _, c := range t.columns {
			if c.pk {
				fmt.Fprintf(&b, "  %s %s [pk]\n", c.name, c.typ)
			} else {
				fmt.Fprintf(&b, "  %s %s\n", c.name, c.typ)
			}
		}
		b.WriteString("}\n")
	}
	if len(rels) > 0 {
		b.WriteString("\n")
	}
	for _, r := range rels {
		fmt.Fprintf(&b, "Ref: %s.%s > %s.%s\n", r.child, r.col, r.parent, r.parentCol)
	}
	return b.String()
}
