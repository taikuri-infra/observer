package manifest

import "testing"

func TestLoad(t *testing.T) {
	yaml := `project: demo
tabs:
  - name: Changes
    kind: generator
    command: "observer gen changes --base main"
    output: changes.md
    renderer: markdown
`
	p, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Project != "demo" {
		t.Fatalf("project = %q", p.Project)
	}
	if len(p.Tabs) != 1 || p.Tabs[0].Name != "Changes" || p.Tabs[0].Output != "changes.md" {
		t.Fatalf("tabs = %+v", p.Tabs)
	}
	if p.Tabs[0].Kind != KindGenerator {
		t.Fatalf("kind = %q", p.Tabs[0].Kind)
	}
}

func TestValidateRejectsUnknownKind(t *testing.T) {
	_, err := Parse([]byte("project: x\ntabs:\n  - name: T\n    kind: bogus\n"))
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestValidateRejectsAbsoluteOutput(t *testing.T) {
	_, err := Parse([]byte("project: x\ntabs:\n  - name: T\n    kind: generator\n    output: /etc/passwd\n"))
	if err == nil {
		t.Fatal("expected error for absolute output path")
	}
}

func TestValidateRejectsDotDotOutput(t *testing.T) {
	_, err := Parse([]byte("project: x\ntabs:\n  - name: T\n    kind: generator\n    output: ../escape.md\n"))
	if err == nil {
		t.Fatal("expected error for output path containing ..")
	}
}

func TestValidateRejectsAbsoluteDir(t *testing.T) {
	_, err := Parse([]byte("project: x\ntabs:\n  - name: T\n    kind: generator\n    dir: /tmp\n"))
	if err == nil {
		t.Fatal("expected error for absolute dir path")
	}
}

func TestValidateRejectsDotDotDir(t *testing.T) {
	_, err := Parse([]byte("project: x\ntabs:\n  - name: T\n    kind: generator\n    dir: ../outside\n"))
	if err == nil {
		t.Fatal("expected error for dir path containing ..")
	}
}

func TestValidateAllowsEmptyOutputAndDir(t *testing.T) {
	// service/link tabs (v2) have no output/dir; generator tabs may also omit
	// dir (meaning "repo root").
	_, err := Parse([]byte("project: x\ntabs:\n  - name: T\n    kind: link\n    url: https://example.com\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
