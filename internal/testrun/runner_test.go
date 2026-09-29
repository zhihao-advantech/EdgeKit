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
}

func (f *fakeDevice) Info() Info { return f.info }

func (f *fakeDevice) Exec(_ context.Context, command string) (string, bool, error) {
	return f.outputs[command], !f.exitBad[command], nil
}

func (f *fakeDevice) Wait(_ context.Context, pattern string, _ time.Duration) (string, error) {
	if f.waits[pattern] {
		return "matched: " + pattern, nil
	}
	return "", fmt.Errorf("timeout")
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
