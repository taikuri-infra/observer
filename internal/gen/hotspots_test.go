package gen

import (
	"testing"
)

func TestHotspots(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "t@t")
	run(t, dir, "config", "user.name", "t")

	if err := writeFile(dir+"/hot.go", "package x\n"); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "add hot.go")

	if err := writeFile(dir+"/cold.go", "package x\n"); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "add cold.go")

	if err := writeFile(dir+"/hot.go", "package x\n\nfunc Hot() {}\n"); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "touch hot.go again")

	stats, err := Hotspots(dir, "", 50)
	if err != nil {
		t.Fatalf("Hotspots: %v", err)
	}

	byPath := map[string]FileStat{}
	for _, s := range stats {
		byPath[s.Path] = s
	}

	hot, ok := byPath["hot.go"]
	if !ok {
		t.Fatalf("hot.go missing from stats: %+v", stats)
	}
	if hot.Commits != 2 || hot.Churn != 2 {
		t.Fatalf("hot.go commits/churn = %+v, want 2", hot)
	}

	cold, ok := byPath["cold.go"]
	if !ok {
		t.Fatalf("cold.go missing from stats: %+v", stats)
	}
	if cold.Commits != 1 {
		t.Fatalf("cold.go commits = %+v, want 1", cold)
	}

	// Sorted by churn descending: hot.go (2) must come before cold.go (1).
	if stats[0].Path != "hot.go" {
		t.Fatalf("expected hot.go first (highest churn), got order: %+v", stats)
	}
}

func TestHotspotsTopCap(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "t@t")
	run(t, dir, "config", "user.name", "t")
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if err := writeFile(dir+"/"+name, "package x\n"); err != nil {
			t.Fatal(err)
		}
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "add three files")

	stats, err := Hotspots(dir, "", 2)
	if err != nil {
		t.Fatalf("Hotspots: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected top=2 to cap results, got %d: %+v", len(stats), stats)
	}
}
