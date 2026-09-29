package testrun

import (
	"strings"
	"testing"
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
