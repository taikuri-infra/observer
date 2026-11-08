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
