package testrun

import (
	"strings"
	"testing"
	"time"

	"edgekit/internal/workspace"
)

func TestStoreDefinitionsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := Store{}
	def := Definition{Name: "boot check", Checks: []Check{{Command: "uname -a", Expect: "Linux"}}}

	rel, err := st.WriteDefinition(def)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.HasSuffix(rel, ".test.json") || !strings.HasPrefix(rel, "tests/") {
		t.Fatalf("unexpected path %q", rel)
	}

	list := st.ListDefinitions()
	if len(list) != 1 || list[0].Name != "boot check" || list[0].Path != rel {
		t.Fatalf("definitions = %+v", list)
	}

	got, err := st.ReadDefinition(rel)
	if err != nil || len(got.Checks) != 1 || got.Checks[0].Command != "uname -a" {
		t.Fatalf("read back = %+v, %v", got, err)
	}

	if _, err := st.ReadDefinition("etc/passwd"); err == nil {
		t.Fatal("reading outside tests/ should be rejected")
	}
}

func TestStoreRunsHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := Store{}

	run := NewRun("run-20260101-1", "demo", "serial-1", Definition{Name: "demo"})
	run.Status = StatusPassed
	if _, err := st.Archive(run); err != nil {
		t.Fatalf("archive: %v", err)
	}

	runs := st.ListRuns()
	if len(runs) != 1 || runs[0].ID != "run-20260101-1" || runs[0].Status != StatusPassed {
		t.Fatalf("runs = %+v", runs)
	}
	got, err := st.ReadRun("run-20260101-1")
	if err != nil || got.Name != "demo" {
		t.Fatalf("read run = %+v, %v", got, err)
	}
	if _, err := st.ReadRun("../etc/passwd"); err == nil {
		t.Fatal("run id with a path separator should be rejected")
	}
}

func TestStorePreviousRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := Store{}

	first := NewRun("run-a", "boot", "serial-1", Definition{Name: "boot"})
	first.CreatedAt = first.CreatedAt.Add(-time.Hour)
	first.Status = StatusPassed
	if _, err := st.Archive(first); err != nil {
		t.Fatal(err)
	}
	second := NewRun("run-b", "boot", "serial-1", Definition{Name: "boot"})
	second.Status = StatusFailed
	if _, err := st.Archive(second); err != nil {
		t.Fatal(err)
	}

	prev, ok := st.Previous("boot", "run-b")
	if !ok || prev.ID != "run-a" {
		t.Fatalf("previous = %+v, %v", prev, ok)
	}
	if _, ok := st.Previous("boot", "run-a"); !ok {
		t.Fatal("previous should find run-b when excluding run-a")
	}
	if _, ok := st.Previous("nope", "run-a"); ok {
		t.Fatal("unknown name should have no previous")
	}
}

func TestStoreListScripts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := workspace.Write("tests/smoke.sh", []byte("echo hi\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Write("tests/notes.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	scripts := (Store{}).ListScripts()
	if len(scripts) != 1 || scripts[0].Path != "tests/smoke.sh" {
		t.Fatalf("scripts = %+v", scripts)
	}
}

func TestStoreDelete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := Store{}

	rel, err := st.WriteDefinition(Definition{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	run := NewRun("run-del", "x", "serial-1", Definition{Name: "x"})
	if _, err := st.Archive(run); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteDefinition(rel); err != nil {
		t.Fatalf("delete definition: %v", err)
	}
	if len(st.ListDefinitions()) != 0 {
		t.Fatal("definition still listed")
	}
	if err := st.DeleteRun("run-del"); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	if len(st.ListRuns()) != 0 {
		t.Fatal("run still listed")
	}

	if err := st.DeleteDefinition("etc/passwd"); err == nil {
		t.Fatal("should reject paths outside tests/")
	}
	if err := st.DeleteRun("../etc"); err == nil {
		t.Fatal("should reject run ids with a separator")
	}
	if err := st.DeleteRun(".."); err == nil {
		t.Fatal("should reject a dot-dot run id")
	}
	// A rejected traversal must leave definitions and archived runs intact.
	if _, err := st.WriteDefinition(Definition{Name: "keep"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRun(".."); err == nil {
		t.Fatal("should reject a dot-dot run id")
	}
	if len(st.ListDefinitions()) != 1 {
		t.Fatal("invalid run id deleted the tests directory")
	}
}
