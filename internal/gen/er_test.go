package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const chatUpSQL = `-- 011_chat.sql
CREATE TABLE IF NOT EXISTS chat_channels (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    name         VARCHAR(100),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_chat_channels_tenant ON chat_channels(tenant_id);

CREATE TABLE IF NOT EXISTS chat_messages (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id   UUID NOT NULL REFERENCES chat_channels(id) ON DELETE CASCADE,
    body         TEXT NOT NULL
);
`

const chatDownSQL = `DROP TABLE IF EXISTS chat_messages;
DROP TABLE IF EXISTS chat_channels;
`

func TestER(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "011_chat.sql"), []byte(chatUpSQL), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "011_chat_down.sql"), []byte(chatDownSQL), 0o644); err != nil {
		t.Fatal(err)
	}

	mermaid, dbml, err := ER(dir)
	if err != nil {
		t.Fatalf("ER: %v", err)
	}

	if !strings.HasPrefix(mermaid, "erDiagram") {
		t.Fatalf("mermaid missing erDiagram header: %s", mermaid)
	}
	if !strings.Contains(mermaid, "chat_channels") {
		t.Fatalf("mermaid missing chat_channels: %s", mermaid)
	}
	if !strings.Contains(mermaid, "chat_messages") {
		t.Fatalf("mermaid missing chat_messages: %s", mermaid)
	}
	if !strings.Contains(mermaid, "chat_channels ||--o{ chat_messages") {
		t.Fatalf("mermaid missing relationship line: %s", mermaid)
	}
	if !strings.Contains(mermaid, "DROP TABLE") {
		// Just making sure the _down.sql content never leaked in as a
		// table — not actually checking for this string, see below.
	}
	if strings.Contains(mermaid, "drop") {
		t.Fatalf("down-migration content leaked into mermaid: %s", mermaid)
	}

	if !strings.Contains(dbml, "Table chat_channels {") {
		t.Fatalf("dbml missing chat_channels table: %s", dbml)
	}
	if !strings.Contains(dbml, "Table chat_messages {") {
		t.Fatalf("dbml missing chat_messages table: %s", dbml)
	}
	if !strings.Contains(dbml, "Ref: chat_messages.channel_id > chat_channels.id") {
		t.Fatalf("dbml missing Ref line: %s", dbml)
	}
	if !strings.Contains(dbml, "id uuid [pk]") {
		t.Fatalf("dbml missing pk annotation: %s", dbml)
	}
}

// TestERGraphModel exercises ERGraphModel against the same chat fixture as
// TestER: chat_channels has no outgoing FK (fkOut=0) and one incoming one
// from chat_messages.channel_id (fkIn=1); chat_messages is the reverse
// (fkOut=1, fkIn=0). The edge direction is child -> parent (chat_messages ->
// chat_channels), matching Go-deps' "from depends on to".
func TestERGraphModel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "011_chat.sql"), []byte(chatUpSQL), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "011_chat_down.sql"), []byte(chatDownSQL), 0o644); err != nil {
		t.Fatal(err)
	}

	g, err := ERGraphModel(dir, "services/api/internal/adapters/postgres/chat_migrations")
	if err != nil {
		t.Fatalf("ERGraphModel: %v", err)
	}

	if g.Meta.Source != "db-er" || g.Meta.SizeBy != "fkIn" || g.Meta.ColorBy != "columns" {
		t.Fatalf("meta = %+v", g.Meta)
	}

	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
		if n.Kind != "table" {
			t.Errorf("node %s kind = %q, want table", n.ID, n.Kind)
		}
		if n.Location.Path != "services/api/internal/adapters/postgres/chat_migrations" {
			t.Errorf("node %s location.path = %q", n.ID, n.Location.Path)
		}
	}

	channels, ok := byID["chat_channels"]
	if !ok {
		t.Fatalf("missing chat_channels node: %+v", g.Nodes)
	}
	if channels.Metrics.Columns != 4 {
		t.Errorf("chat_channels columns = %d, want 4", channels.Metrics.Columns)
	}
	if channels.Metrics.FKOut != 0 || channels.Metrics.FKIn != 1 {
		t.Errorf("chat_channels fk metrics = %+v, want fkOut=0 fkIn=1", channels.Metrics)
	}

	messages, ok := byID["chat_messages"]
	if !ok {
		t.Fatalf("missing chat_messages node: %+v", g.Nodes)
	}
	if messages.Metrics.Columns != 3 {
		t.Errorf("chat_messages columns = %d, want 3", messages.Metrics.Columns)
	}
	if messages.Metrics.FKOut != 1 || messages.Metrics.FKIn != 0 {
		t.Errorf("chat_messages fk metrics = %+v, want fkOut=1 fkIn=0", messages.Metrics)
	}

	if len(g.Edges) != 1 {
		t.Fatalf("edges = %+v, want 1", g.Edges)
	}
	if e := g.Edges[0]; e.From != "chat_messages" || e.To != "chat_channels" {
		t.Errorf("edge = %+v, want chat_messages -> chat_channels", e)
	}

	// Each table node carries its full column list (for the ER card), with
	// PK/FK flags — chat_messages has a PK (id) and an FK (channel_id).
	if messages.Table == nil {
		t.Fatalf("chat_messages.Table is nil, want populated column list")
	}
	wantCols := map[string]TableColumn{
		"id":         {Name: "id", Type: "uuid", PK: true, FK: false},
		"channel_id": {Name: "channel_id", Type: "uuid", PK: false, FK: true},
		"body":       {Name: "body", Type: "text", PK: false, FK: false},
	}
	if len(messages.Table.Columns) != len(wantCols) {
		t.Fatalf("chat_messages columns = %+v, want %d", messages.Table.Columns, len(wantCols))
	}
	for _, c := range messages.Table.Columns {
		want, ok := wantCols[c.Name]
		if !ok {
			t.Errorf("unexpected column %q", c.Name)
			continue
		}
		if c != want {
			t.Errorf("column %q = %+v, want %+v", c.Name, c, want)
		}
	}

	// A non-table node (there are none here) would omit Table — guard the
	// chat_channels PK flag too (id is PRIMARY KEY, nothing is FK).
	if channels.Table == nil {
		t.Fatalf("chat_channels.Table is nil")
	}
	var chID TableColumn
	for _, c := range channels.Table.Columns {
		if c.Name == "id" {
			chID = c
		}
		if c.FK {
			t.Errorf("chat_channels has an unexpected FK column: %+v", c)
		}
	}
	if !chID.PK || chID.Type != "uuid" {
		t.Errorf("chat_channels.id = %+v, want PK uuid", chID)
	}
}

// TestERTableLevelFK covers a table-level (here composite) FOREIGN KEY
// constraint line — `FOREIGN KEY (a, b) REFERENCES parent (a, b)` — which the
// excludedLinePrefixes filter would drop before the REFERENCES scan if the
// scan didn't also run on excluded constraint lines. The edge must still
// appear, keyed on the first FK column.
func TestERTableLevelFK(t *testing.T) {
	const sql = `CREATE TABLE parent (
    a INTEGER NOT NULL,
    b INTEGER NOT NULL,
    PRIMARY KEY (a, b)
);

CREATE TABLE child (
    id UUID PRIMARY KEY,
    a INTEGER NOT NULL,
    b INTEGER NOT NULL,
    FOREIGN KEY (a, b) REFERENCES parent (a, b)
);
`
	tables, rels := parseSQL(sql)
	if len(tables) != 2 {
		t.Fatalf("tables = %+v, want 2", tables)
	}
	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 1 (the table-level composite FK)", rels)
	}
	r := rels[0]
	if r.parent != "parent" || r.child != "child" {
		t.Fatalf("rel = %+v, want child->parent", r)
	}
	if r.col != "a" {
		t.Fatalf("rel.col = %q, want a (first FK column)", r.col)
	}
}

// TestERSchemaQualified covers schema-qualified names: `CREATE TABLE
// public.users (...)` must still produce a "users" table (not be dropped),
// and `REFERENCES public.users(id)` must resolve the parent to "users" (not
// a phantom "public" node).
func TestERSchemaQualified(t *testing.T) {
	const sql = `CREATE TABLE public.users (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL
);

CREATE TABLE public.sessions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES public.users(id)
);
`
	tables, rels := parseSQL(sql)
	names := map[string]bool{}
	for _, tbl := range tables {
		names[tbl.name] = true
	}
	if !names["users"] || !names["sessions"] {
		t.Fatalf("tables = %+v, want users+sessions (schema stripped)", tables)
	}
	if names["public"] {
		t.Fatalf("a phantom 'public' table was created: %+v", tables)
	}
	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 1", rels)
	}
	if rels[0].parent != "users" {
		t.Fatalf("rel.parent = %q, want users (schema stripped, not 'public')", rels[0].parent)
	}
	if rels[0].child != "sessions" {
		t.Fatalf("rel.child = %q, want sessions", rels[0].child)
	}
}

// TestERLooseTerminator covers CREATE TABLE closings other than exactly
// "\n);": an indented ")", trailing table options (`) STRICT;`), and a
// PARTITION BY clause after the paren.
func TestERLooseTerminator(t *testing.T) {
	const sql = `CREATE TABLE a (
    id INTEGER PRIMARY KEY
  );

CREATE TABLE b (
    id INTEGER PRIMARY KEY
) STRICT;

CREATE TABLE c (
    id INTEGER PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL
) PARTITION BY RANGE (created_at);
`
	tables, _ := parseSQL(sql)
	got := map[string]bool{}
	for _, tbl := range tables {
		got[tbl.name] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !got[want] {
			t.Fatalf("table %q missing (loose terminator not matched): %+v", want, tables)
		}
	}
}

func TestERSkipsDownMigrations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "001_x_down.sql"), []byte("CREATE TABLE should_not_appear (\n    id UUID PRIMARY KEY\n);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mermaid, _, err := ER(dir)
	if err != nil {
		t.Fatalf("ER: %v", err)
	}
	if strings.Contains(mermaid, "should_not_appear") {
		t.Fatalf("down-migration table leaked into output: %s", mermaid)
	}
}
