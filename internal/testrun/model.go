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

import "time"

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

// Check is one step of a test case: run a command and/or expect output.
type Check struct {
	Name      string `json:"name,omitempty"`
	Command   string `json:"command,omitempty"`   // run on the target session
	Expect    string `json:"expect,omitempty"`    // regexp the output must (not) match
	ExpectNot bool   `json:"expectNot,omitempty"` // invert Expect
	ExitZero  bool   `json:"exitZero,omitempty"`  // require a zero exit code
	TimeoutMS int    `json:"timeoutMs,omitempty"` // wait budget for Expect
}

// Definition is a reusable test case. It can be entered by hand in the UI or
// kept as a file in the workspace (tests/<name>.test.json).
type Definition struct {
	Name       string  `json:"name"`
	TargetHint string  `json:"targetHint,omitempty"`
	Checks     []Check `json:"checks"`
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
func (r *Run) PhaseOutput(key string) string {
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
