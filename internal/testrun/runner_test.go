package testrun

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeDevice is a scripted Device for tests.
type fakeDevice struct {
	info    Info
	outputs map[string]string
	exitBad map[string]bool
	waits   map[string]bool

	scriptOut string
	scriptBad bool
	scriptErr error
	gotScript string
}

func (f *fakeDevice) Info() Info { return f.info }

func (f *fakeDevice) Exec(_ context.Context, command string, _ time.Duration) (string, bool, error) {
	return f.outputs[command], !f.exitBad[command], nil
}

func (f *fakeDevice) RunScript(_ context.Context, script string, _ time.Duration) (string, bool, error) {
	f.gotScript = script
	if f.scriptErr != nil {
		return "", false, f.scriptErr
	}
	return f.scriptOut, !f.scriptBad, nil
}

func (f *fakeDevice) Wait(_ context.Context, pattern string, _ time.Duration) (string, error) {
	if f.waits[pattern] {
		return "matched: " + pattern, nil
	}
	return "", fmt.Errorf("timeout")
}

func (f *fakeDevice) WaitAbsent(_ context.Context, pattern string, _ time.Duration) error {
	if f.waits[pattern] {
		return fmt.Errorf("forbidden output appeared")
	}
	return nil
}

type fakeArchiver struct {
	path   string
	called bool
}

func (a *fakeArchiver) Archive(*Run) (string, error) {
	a.called = true
	return a.path, nil
}

func connected() *fakeDevice {
	return &fakeDevice{
		info:    Info{ID: "serial-1", Kind: "serial", Label: "ttyUSB0", Connected: true},
		outputs: map[string]string{"uname -a": "Linux board 5.15", "true": ""},
		exitBad: map[string]bool{},
		waits:   map[string]bool{"Linux board": true},
	}
}

func TestRunAllPasses(t *testing.T) {
	dev := connected()
	arch := &fakeArchiver{path: "tests/runs/r1"}
	def := Definition{Name: "boot", Checks: []Check{
		{Name: "kernel", Command: "uname -a", Expect: "Linux board"},
		{Name: "exit", Command: "true", ExitZero: true},
	}}
	r := New("r1", "boot", "serial-1", def, dev, arch, nil)

	r.RunAll(context.Background())
	run := r.Snapshot()

	if run.Status != StatusPassed {
		t.Fatalf("run status = %s, want passed", run.Status)
	}
	for _, key := range PhaseKeys {
		if p := run.Phase(key); p.Status != StatusPassed {
			t.Fatalf("phase %s = %s", key, p.Status)
		}
	}
	if p := run.Phase(PhaseRun); len(p.Checks) != 2 || p.Checks[0].Status != CheckPass || p.Checks[1].Status != CheckPass {
		t.Fatalf("checks = %+v", p.Checks)
	}
	if run.ArchivedPath != "tests/runs/r1" {
		t.Fatalf("archived path = %q", run.ArchivedPath)
	}
	if !arch.called {
		t.Fatal("archiver not called")
	}
	if !strings.Contains(run.PhaseOutput(PhaseGenerate), "测试报告") ||
		!strings.Contains(run.PhaseOutput(PhaseGenerate), "通过") {
		t.Fatalf("report missing content:\n%s", run.PhaseOutput(PhaseGenerate))
	}
}

func TestRunFailsOnMissingOutput(t *testing.T) {
	dev := connected()
	def := Definition{Name: "boot", Checks: []Check{
		{Name: "banner", Command: "uname -a", Expect: "NOT-PRESENT"},
	}}
	r := New("r2", "boot", "serial-1", def, dev, &fakeArchiver{}, nil)

	r.RunAll(context.Background())
	run := r.Snapshot()

	if run.Status != StatusFailed {
		t.Fatalf("run status = %s, want failed", run.Status)
	}
	p := run.Phase(PhaseRun)
	if p.Status != StatusFailed || len(p.Checks) != 1 || p.Checks[0].Status != CheckFail {
		t.Fatalf("run phase = %+v", p)
	}
	// Failure still generates and archives the evidence.
	if run.Phase(PhaseGenerate).Status != StatusPassed || run.Phase(PhaseArchive).Status != StatusPassed {
		t.Fatalf("generate/archive should still run: %+v", run.Phases)
	}
}

func TestRunFailsOnExitCode(t *testing.T) {
	dev := connected()
	dev.exitBad["false"] = true
	def := Definition{Name: "exit", Checks: []Check{{Command: "false", ExitZero: true}}}
	r := New("r3", "exit", "serial-1", def, dev, &fakeArchiver{}, nil)

	r.RunAll(context.Background())
	if got := r.Snapshot().Status; got != StatusFailed {
		t.Fatalf("status = %s, want failed", got)
	}
}

func TestConnectFailureSkipsRun(t *testing.T) {
	dev := &fakeDevice{
		info:    Info{ID: "serial-9", Kind: "serial", Label: "ttyUSB9", Connected: false},
		outputs: map[string]string{},
		exitBad: map[string]bool{},
		waits:   map[string]bool{},
	}
	arch := &fakeArchiver{path: "tests/runs/r4"}
	def := Definition{Name: "x", Checks: []Check{{Command: "true"}}}
	r := New("r4", "x", "serial-9", def, dev, arch, nil)

	r.RunAll(context.Background())
	run := r.Snapshot()

	if run.Phase(PhaseConnect).Status != StatusFailed {
		t.Fatalf("connect = %+v", run.Phase(PhaseConnect))
	}
	if run.Phase(PhaseRun).Status != StatusSkipped {
		t.Fatalf("run should be skipped, got %+v", run.Phase(PhaseRun))
	}
	if run.Phase(PhaseGenerate).Status != StatusPassed || run.Phase(PhaseArchive).Status != StatusPassed {
		t.Fatalf("generate/archive should still run: %+v", run.Phases)
	}
	if !arch.called {
		t.Fatal("failed connect should still archive")
	}
}

func TestUpdatesArePublished(t *testing.T) {
	dev := connected()
	var updates int
	def := Definition{Name: "u", Checks: []Check{{Command: "true"}}}
	r := New("r5", "u", "serial-1", def, dev, &fakeArchiver{}, func(Run) { updates++ })

	r.RunAll(context.Background())
	if updates < 8 { // 4 phases × begin/end
		t.Fatalf("expected many updates, got %d", updates)
	}
}

func TestStoreArchiveWritesWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	run := NewRun("20260101-000000-1", "demo", "serial-1", Definition{Name: "demo"})
	if p := run.Phase(PhaseGenerate); p != nil {
		p.Output = "# 报告"
	}
	rel, err := (Store{}).Archive(run)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if !strings.HasPrefix(rel, "tests/runs/") {
		t.Fatalf("unexpected path %q", rel)
	}
	if run.PhaseOutput(PhaseGenerate) != "# 报告" {
		t.Fatalf("report not kept on run")
	}
}

func TestReportRegressionComparison(t *testing.T) {
	dev := connected()
	def := Definition{Name: "boot", Checks: []Check{{Command: "uname -a", Expect: "NOPE"}}}

	prev := *NewRun("r0", "boot", "serial-1", Definition{Name: "boot"})
	prev.Status = StatusPassed

	r := New("r1", "boot", "serial-1", def, dev, &fakeArchiver{}, nil)
	r.SetPrevious(&prev)
	r.RunAll(context.Background())

	report := r.Snapshot().PhaseOutput(PhaseGenerate)
	if !strings.Contains(report, "回归") {
		t.Fatalf("report should flag the regression:\n%s", report)
	}
}

func TestReportFixComparison(t *testing.T) {
	dev := connected()
	def := Definition{Name: "boot", Checks: []Check{{Command: "uname -a", Expect: "Linux"}}}

	prev := *NewRun("r0", "boot", "serial-1", Definition{Name: "boot"})
	prev.Status = StatusFailed

	r := New("r1", "boot", "serial-1", def, dev, &fakeArchiver{}, nil)
	r.SetPrevious(&prev)
	r.RunAll(context.Background())

	if report := r.Snapshot().PhaseOutput(PhaseGenerate); !strings.Contains(report, "修复") {
		t.Fatalf("report should note the fix:\n%s", report)
	}
}

func TestRunScriptCase(t *testing.T) {
	dev := connected()
	dev.scriptOut = "smoke: ALL TESTS PASSED\n"
	def := Definition{Name: "smoke", Checks: []Check{{Script: "tests/smoke.sh", Expect: "PASSED", ExitZero: true}}}
	r := New("r-s", "smoke", "serial-1", def, dev, &fakeArchiver{path: "tests/runs/r-s"}, nil)

	r.RunAll(context.Background())
	run := r.Snapshot()

	if run.Status != StatusPassed {
		t.Fatalf("script run = %s (%+v)", run.Status, run.Phase(PhaseRun))
	}
	if dev.gotScript != "tests/smoke.sh" {
		t.Fatalf("script not used: %q", dev.gotScript)
	}
	p := run.Phase(PhaseRun)
	if len(p.Checks) != 1 || p.Checks[0].Status != CheckPass || !strings.Contains(p.Checks[0].Command, "tests/smoke.sh") {
		t.Fatalf("script check = %+v", p.Checks)
	}
}

func TestRunScriptCaseFails(t *testing.T) {
	dev := connected()
	dev.scriptOut = "smoke: FAILED\n"
	def := Definition{Name: "smoke", Checks: []Check{{Script: "tests/smoke.sh", Expect: "PASSED"}}}
	r := New("r-s2", "smoke", "serial-1", def, dev, &fakeArchiver{}, nil)

	r.RunAll(context.Background())
	if r.Snapshot().Status != StatusFailed {
		t.Fatalf("expected failure when the script output misses the expectation")
	}
}

func TestRunScriptExitCode(t *testing.T) {
	dev := connected()
	dev.scriptBad = true
	def := Definition{Name: "smoke", Checks: []Check{{Script: "tests/smoke.sh", ExitZero: true}}}
	r := New("r-s3", "smoke", "serial-1", def, dev, &fakeArchiver{}, nil)

	r.RunAll(context.Background())
	if r.Snapshot().Status != StatusFailed {
		t.Fatal("non-zero script exit should fail when exitZero is set")
	}
}

func TestExpectNotWaitsForTheWholeWindow(t *testing.T) {
	dev := connected()
	def := Definition{Name: "quiet", Checks: []Check{{Expect: "PANIC", ExpectNot: true, TimeoutMS: 200}}}
	r := New("r-not", "quiet", "serial-1", def, dev, &fakeArchiver{}, nil)
	r.RunAll(context.Background())
	if got := r.Snapshot().Status; got != StatusPassed {
		t.Fatalf("absent output should pass, got %s", got)
	}

	dev = connected()
	dev.waits["PANIC"] = true
	r = New("r-not2", "quiet", "serial-1", def, dev, &fakeArchiver{}, nil)
	r.RunAll(context.Background())
	if got := r.Snapshot().Status; got != StatusFailed {
		t.Fatalf("forbidden output should fail, got %s", got)
	}
}

func TestCleanOutput(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\r\nb", "a\nb"},
		{"line1\rline2", "line1\nline2"},
		{"\x1b[32mOK\x1b[0m done", "OK done"},
		{"tail\n", "tail"},
	}
	for _, tc := range cases {
		if got := cleanOutput(tc.in); got != tc.want {
			t.Fatalf("cleanOutput(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDefinitionValidate(t *testing.T) {
	cases := []struct {
		name string
		def  Definition
		ok   bool
	}{
		{"empty", Definition{}, false},
		{"empty check", Definition{Checks: []Check{{}}}, false},
		{"command ok", Definition{Checks: []Check{{Command: "true"}}}, true},
		{"expect only ok", Definition{Checks: []Check{{Expect: "x"}}}, true},
		{"script ok", Definition{Checks: []Check{{Script: "tests/a.sh"}}}, true},
		{"both command and script", Definition{Checks: []Check{{Command: "true", Script: "tests/a.sh"}}}, false},
		{"expectNot without expect", Definition{Checks: []Check{{Command: "true", ExpectNot: true}}}, false},
		{"exitZero without runner", Definition{Checks: []Check{{Expect: "x", ExitZero: true}}}, false},
	}
	for _, tc := range cases {
		err := tc.def.Validate()
		if tc.ok && err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
	}
}

func TestRejectsInvalidDefinitionWithoutRunning(t *testing.T) {
	dev := connected()
	arch := &fakeArchiver{}
	r := New("r-invalid", "bad", "serial-1", Definition{Name: "bad", Checks: []Check{{}}}, dev, arch, nil)
	r.RunAll(context.Background())
	run := r.Snapshot()
	if run.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", run.Status)
	}
	if run.Phase(PhaseRun).Status != StatusFailed {
		t.Fatalf("run phase = %s", run.Phase(PhaseRun).Status)
	}
	// An invalid case fails fast but is still reported and archived for audit.
	if !arch.called {
		t.Fatal("failed invalid run should still archive")
	}
}

func TestAbandonedRunDoesNotArchive(t *testing.T) {
	dev := connected()
	arch := &fakeArchiver{path: "tests/runs/x"}
	r := New("r-abandon", "gone", "serial-1", Definition{Name: "gone", Checks: []Check{{Command: "true"}}}, dev, arch, nil)
	r.Abandon()
	r.RunAll(context.Background())
	if arch.called {
		t.Fatal("an abandoned (deleted) run must not archive")
	}
}

// blockingDevice blocks Exec until the context is cancelled, to exercise abort.
type blockingDevice struct{ fakeDevice }

func (b *blockingDevice) Exec(ctx context.Context, command string, _ time.Duration) (string, bool, error) {
	<-ctx.Done()
	return "", false, ctx.Err()
}

func TestAbortMarksAbortedNotPassed(t *testing.T) {
	dev := &blockingDevice{fakeDevice: *connected()}
	r := New("r-abort", "slow", "serial-1", Definition{Name: "slow", Checks: []Check{{Command: "sleep 100"}}}, dev, &fakeArchiver{}, nil)
	done := make(chan struct{})
	go func() { r.RunAll(context.Background()); close(done) }()
	time.Sleep(30 * time.Millisecond)
	r.Abort()
	<-done
	run := r.Snapshot()
	if run.Status != StatusAborted {
		t.Fatalf("status = %s, want aborted", run.Status)
	}
	if run.Phase(PhaseRun).Status != StatusAborted {
		t.Fatalf("run phase = %s, want aborted", run.Phase(PhaseRun).Status)
	}
}

func TestRunPhaseEnforcesOrder(t *testing.T) {
	r := New("r-order", "order", "serial-1", Definition{Name: "order", Checks: []Check{{Command: "true"}}}, connected(), &fakeArchiver{}, nil)
	if err := r.RunPhase(context.Background(), PhaseArchive); err == nil {
		t.Fatal("archive must not run before connect/run/generate")
	}
	snap := r.Snapshot()
	if got := snap.Phase(PhaseArchive).Status; got != StatusPending {
		t.Fatalf("invalid phase request changed state: %s", got)
	}
}
