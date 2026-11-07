package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	createTableRe = regexp.MustCompile(`(?is)CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+([a-zA-Z_][a-zA-Z0-9_]*)\s*\((.*?)\n\);`)
	columnRe      = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)\s+([A-Za-z0-9_]+)`)
	referencesRe  = regexp.MustCompile(`(?i)REFERENCES\s+([a-zA-Z_][a-zA-Z0-9_]*)\s*(?:\(\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\))?`)
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
				parentCol := rm[2]
				if parentCol == "" {
					parentCol = "id"
				}
				rels = append(rels, erRel{
					parent:    strings.ToLower(rm[1]),
					parentCol: strings.ToLower(parentCol),
					child:     name,
					col:       col,
				})
			}
		}
		tables = append(tables, erTable{name: name, columns: cols})
	}
	return tables, rels
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
