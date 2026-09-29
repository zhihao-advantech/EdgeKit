package kits

import (
	"context"
	"strings"
	"testing"

	"edgekit/internal/kit"
	"edgekit/internal/testrun"
)

type fakeTest struct {
	defs   []testrun.DefinitionRef
	runs   []testrun.RunSummary
	result testrun.Run
	report string

	gotSession string
	gotPath    string
	gotDef     *testrun.Definition
}

func (f *fakeTest) Definitions() []testrun.DefinitionRef { return f.defs }
func (f *fakeTest) Runs() []testrun.RunSummary           { return f.runs }
func (f *fakeTest) Report(string) (string, error)        { return f.report, nil }
func (f *fakeTest) Run(_ context.Context, session, path string, def *testrun.Definition) (testrun.Run, error) {
	f.gotSession, f.gotPath, f.gotDef = session, path, def
	return f.result, nil
}

func testRegistry(t *testing.T, ft *fakeTest) *kit.Registry {
	t.Helper()
	reg := kit.NewRegistry()
	reg.Register(testKit{ft})
	reg.SetEvent(kit.DeviceKindEvent("serial"), true)
	return reg
}

func finishedRun() testrun.Run {
	run := testrun.NewRun("run-1", "smoke", "serial-1", testrun.Definition{Name: "smoke"})
	run.Status = testrun.StatusPassed
	if p := run.Phase(testrun.PhaseRun); p != nil {
		p.Status = testrun.StatusPassed
		p.Checks = []testrun.CheckResult{{Index: 0, Name: "ok", Command: "true", Status: testrun.CheckPass}}
	}
	if p := run.Phase(testrun.PhaseGenerate); p != nil {
		p.Output = "# 报告\n结论：通过"
	}
	return *run
}

func TestTestKitListAndRun(t *testing.T) {
	ft := &fakeTest{
		defs:   []testrun.DefinitionRef{{Path: "tests/smoke.test.json", Name: "smoke"}},
		runs:   []testrun.RunSummary{{ID: "run-1", Name: "smoke", Status: "passed"}},
		result: finishedRun(),
		report: "# 报告内容",
	}
	reg := testRegistry(t, ft)

	// test_list
	out, err := tool(t, reg, "test_list").Call(context.Background(), nil)
	if err != nil {
		t.Fatalf("test_list: %v", err)
	}
	if !strings.Contains(out, "smoke") || !strings.Contains(out, "run-1") {
		t.Fatalf("test_list output = %q", out)
	}

	// test_run with a workspace path
	out, err = tool(t, reg, "test_run").Call(context.Background(), map[string]any{
		"path": "tests/smoke.test.json", "session": "serial-2",
	})
	if err != nil {
		t.Fatalf("test_run: %v", err)
	}
	if ft.gotPath != "tests/smoke.test.json" || ft.gotSession != "serial-2" || ft.gotDef != nil {
		t.Fatalf("run args = path %q session %q def %v", ft.gotPath, ft.gotSession, ft.gotDef)
	}
	if !strings.Contains(out, "通过") || !strings.Contains(out, "run-1") {
		t.Fatalf("test_run output = %q", out)
	}

	// test_run with an inline one-off check
	_, err = tool(t, reg, "test_run").Call(context.Background(), map[string]any{
		"command": "uname -a", "expect": "Linux", "timeout_ms": 2000, "exit_zero": true,
	})
	if err != nil {
		t.Fatalf("inline test_run: %v", err)
	}
	if ft.gotDef == nil || len(ft.gotDef.Checks) != 1 || ft.gotDef.Checks[0].Command != "uname -a" {
		t.Fatalf("inline def = %+v", ft.gotDef)
	}
	if !ft.gotDef.Checks[0].ExitZero || ft.gotDef.Checks[0].TimeoutMS != 2000 {
		t.Fatalf("inline check flags = %+v", ft.gotDef.Checks[0])
	}

	// test_report
	out, err = tool(t, reg, "test_report").Call(context.Background(), map[string]any{"run": "run-1"})
	if err != nil || !strings.Contains(out, "报告内容") {
		t.Fatalf("test_report = %q, %v", out, err)
	}
}

func TestTestKitRunRequiresInput(t *testing.T) {
	reg := testRegistry(t, &fakeTest{})
	if _, err := tool(t, reg, "test_run").Call(context.Background(), map[string]any{}); err == nil {
		t.Fatal("test_run without path/command should fail")
	}
}
