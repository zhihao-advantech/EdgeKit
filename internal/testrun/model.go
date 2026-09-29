// Package testrun models and executes an EdgeKit device test run: connect a
// board, run the test case's checks, generate a report and archive the result
// into the local workspace.
//
// A run has a fixed pipeline of four phases (connect → run → generate →
// archive); the test case itself is a list of checks (command + expected
// output) executed inside the "run" phase. The runner is bound to one device
// session and drives it through the host's capability layer, so all output
// still lands on the device timeline.
package testrun

import (
	"fmt"
	"strings"
	"time"
)

// Phase keys, in pipeline order.
const (
	PhaseConnect  = "connect"
	PhaseRun      = "run"
	PhaseGenerate = "generate"
	PhaseArchive  = "archive"
)

// PhaseKeys is the fixed order the UI shows.
var PhaseKeys = []string{PhaseConnect, PhaseRun, PhaseGenerate, PhaseArchive}

// Run / phase status values.
const (
	StatusIdle    = "idle"
	StatusPending = "pending"
	StatusRunning = "running"
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
	StatusAborted = "aborted"
)

// Check status values.
const (
	CheckPass = "pass"
	CheckFail = "fail"
	CheckSkip = "skip"
)

// Check is one step of a test case: it runs a shell command, or a workspace
// script (Script), and/or expects matching output.
type Check struct {
	Name      string `json:"name,omitempty"`
	Command   string `json:"command,omitempty"` // shell command on the target
	Script    string `json:"script,omitempty"`  // workspace script (tests/*.sh) to run instead
	Expect    string `json:"expect,omitempty"`  // regexp the output must (not) match
	ExpectNot bool   `json:"expectNot,omitempty"`
	ExitZero  bool   `json:"exitZero,omitempty"`  // require a zero exit code
	TimeoutMS int    `json:"timeoutMs,omitempty"` // wait budget
}

// Definition is a reusable test case. It can be entered by hand in the UI or
// kept as a file in the workspace (tests/<name>.test.json).
type Definition struct {
	Name       string  `json:"name"`
	TargetHint string  `json:"targetHint,omitempty"`
	Checks     []Check `json:"checks"`
}

// Validate rejects definitions that could produce a meaningless pass (for
// example, a completely empty check row).
func (d Definition) Validate() error {
	if len(d.Checks) == 0 {
		return fmt.Errorf("测试定义至少需要一条检查项")
	}
	for i, c := range d.Checks {
		command := strings.TrimSpace(c.Command) != ""
		script := strings.TrimSpace(c.Script) != ""
		expect := strings.TrimSpace(c.Expect) != ""
		if command && script {
			return fmt.Errorf("检查项 %d 不能同时指定指令和脚本", i+1)
		}
		if !command && !script && !expect {
			return fmt.Errorf("检查项 %d 为空：请填写指令、选择脚本或设置期望输出", i+1)
		}
		if c.ExpectNot && !expect {
			return fmt.Errorf("检查项 %d 启用了反向期望但没有填写期望输出", i+1)
		}
		if c.ExitZero && !command && !script {
			return fmt.Errorf("检查项 %d 要求退出码为 0，但没有指令或脚本", i+1)
		}
		if c.TimeoutMS < 0 {
			return fmt.Errorf("检查项 %d 的超时不能为负数", i+1)
		}
	}
	return nil
}

// CheckResult is one executed check.
type CheckResult struct {
	Index   int    `json:"index"`
	Name    string `json:"name,omitempty"`
	Command string `json:"command"`
	Expect  string `json:"expect,omitempty"`
	Status  string `json:"status"` // pass | fail | skip
	Output  string `json:"output,omitempty"`
	Err     string `json:"err,omitempty"`
}

// Phase is one of the four pipeline steps.
type Phase struct {
	Key       string        `json:"key"`
	Status    string        `json:"status"` // pending | running | passed | failed | skipped
	StartedAt time.Time     `json:"startedAt,omitempty"`
	EndedAt   time.Time     `json:"endedAt,omitempty"`
	Summary   string        `json:"summary,omitempty"`
	Output    string        `json:"output,omitempty"`
	Artifact  string        `json:"artifact,omitempty"`
	Checks    []CheckResult `json:"checks,omitempty"`
}

// Run is one execution of a Definition against one device session.
type Run struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	SessionID     string     `json:"sessionId"`
	DefinitionRef string     `json:"definitionRef,omitempty"`
	Status        string     `json:"status"` // idle | running | passed | failed | aborted
	Definition    Definition `json:"definition"`
	Phases        []Phase    `json:"phases"`
	CreatedAt     time.Time  `json:"createdAt"`
	EndedAt       time.Time  `json:"endedAt,omitempty"`
	ArchivedPath  string     `json:"archivedPath,omitempty"`
}

// NewRun builds an idle run with the four phases pending.
func NewRun(id, name, sessionID string, def Definition) *Run {
	phases := make([]Phase, 0, len(PhaseKeys))
	for _, k := range PhaseKeys {
		phases = append(phases, Phase{Key: k, Status: StatusPending})
	}
	return &Run{
		ID:         id,
		Name:       name,
		SessionID:  sessionID,
		Status:     StatusIdle,
		Definition: def,
		Phases:     phases,
		CreatedAt:  time.Now(),
	}
}

// Phase returns a pointer to the phase with the given key (nil when unknown).
func (r *Run) Phase(key string) *Phase {
	for i := range r.Phases {
		if r.Phases[i].Key == key {
			return &r.Phases[i]
		}
	}
	return nil
}

// PhaseOutput returns the remembered output of a phase.
func (r Run) PhaseOutput(key string) string {
	if p := r.Phase(key); p != nil {
		return p.Output
	}
	return ""
}

// Finished reports whether the pipeline has run to the end. The verdict
// (Status) is decided by the run phase, so completion is signalled by the last
// phase (archive) reaching a terminal state.
func (r *Run) Finished() bool {
	if p := r.Phase(PhaseArchive); p != nil {
		switch p.Status {
		case StatusPassed, StatusFailed, StatusSkipped, StatusAborted:
			return true
		}
	}
	return false
}
