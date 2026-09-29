package testrun

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Info describes the device session a run is bound to.
type Info struct {
	ID        string
	Kind      string // serial | ssh | desktop | local
	Label     string
	Connected bool
}

// Device is the target of a run: the host implements it over the session's
// capabilities (SSH exec / serial capture + the device timeline).
type Device interface {
	Info() Info
	// Exec runs a command on the device and reports the output and whether it
	// exited successfully.
	Exec(ctx context.Context, command string, timeout time.Duration) (output string, exitOK bool, err error)
	// Wait blocks until pattern (a regexp) appears in the device output.
	Wait(ctx context.Context, pattern string, timeout time.Duration) (matched string, err error)
	// WaitAbsent succeeds only when pattern stays absent for timeout.
	WaitAbsent(ctx context.Context, pattern string, timeout time.Duration) error
	// RunScript runs a workspace script (tests/*.sh) on the device and reports
	// its output and whether it exited successfully.
	RunScript(ctx context.Context, script string, timeout time.Duration) (output string, exitOK bool, err error)
}

// Archiver persists a finished run. It returns a workspace-relative path.
type Archiver interface {
	Archive(run *Run) (string, error)
}

// Runner executes one Run against one Device, publishing state after every
// change so the UI (and later an agent) can follow along.
type Runner struct {
	mu             sync.Mutex
	run            Run
	dev            Device
	store          Archiver
	prev           *Run // previous run of the same name, for the report comparison
	onUpdate       func(Run)
	cancel         context.CancelFunc
	busy           bool
	abandoned      bool // deleted: stop publishing and never archive
	abortRequested bool
	idle           chan struct{}
}

// New creates a runner for def bound to dev.
func New(id, name, sessionID string, def Definition, dev Device, store Archiver, onUpdate func(Run)) *Runner {
	idle := make(chan struct{})
	close(idle)
	r := &Runner{dev: dev, store: store, onUpdate: onUpdate, idle: idle}
	r.run = *NewRun(id, name, sessionID, def)
	return r
}

// SetPrevious records the previous run of the same name so the generated report
// can call out regressions and fixes.
func (r *Runner) SetPrevious(prev *Run) {
	r.mu.Lock()
	r.prev = prev
	r.mu.Unlock()
}

// Snapshot returns a copy of the current run state.
func (r *Runner) Snapshot() Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

func (r *Runner) snapshotLocked() Run {
	out := r.run
	out.Phases = append([]Phase(nil), r.run.Phases...)
	return out
}

func (r *Runner) update() {
	r.mu.Lock()
	if r.abandoned {
		r.mu.Unlock()
		return
	}
	snap := r.snapshotLocked()
	r.mu.Unlock()
	if r.onUpdate != nil {
		r.onUpdate(snap)
	}
}

// Abandon marks the run as deleted. It stops publishing and prevents a late
// archive, so a deleted run cannot reappear in the UI or on disk.
func (r *Runner) Abandon() {
	r.mu.Lock()
	r.abandoned = true
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runner) isAbandoned() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.abandoned
}

// Abort cancels a running phase (no-op when idle).
func (r *Runner) Abort() {
	r.mu.Lock()
	r.abortRequested = true
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runner) abortRequestedNow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.abortRequested
}

// markAborted records a cancelled run: the verdict is preserved if the run
// phase already produced one, otherwise the run is marked aborted and any
// still-running phase is closed out.
func (r *Runner) markAborted() {
	r.mu.Lock()
	if r.run.Status != StatusPassed && r.run.Status != StatusFailed {
		r.run.Status = StatusAborted
	}
	for i := range r.run.Phases {
		if r.run.Phases[i].Status == StatusRunning {
			r.run.Phases[i].Status = StatusAborted
			r.run.Phases[i].EndedAt = time.Now()
			if r.run.Phases[i].Summary == "" {
				r.run.Phases[i].Summary = "已中止"
			}
		}
	}
	r.mu.Unlock()
	r.update()
}

// Running reports whether a phase is currently executing.
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy
}

// WaitIdle waits until the current phase exits. An orchestrating RunAll may be
// between phases while idle; Abandon makes its next phase check return before
// it can archive.
func (r *Runner) WaitIdle(ctx context.Context) error {
	r.mu.Lock()
	if !r.busy {
		r.mu.Unlock()
		return nil
	}
	idle := r.idle
	r.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RunPhase executes one phase of the pipeline.
func (r *Runner) RunPhase(ctx context.Context, key string) error {
	r.mu.Lock()
	if r.abandoned {
		r.mu.Unlock()
		return fmt.Errorf("测试已删除")
	}
	if r.busy {
		r.mu.Unlock()
		return fmt.Errorf("测试正在运行中")
	}
	if r.run.Phase(key) == nil {
		r.mu.Unlock()
		return fmt.Errorf("未知阶段: %s", key)
	}
	if err := r.prereqLocked(key); err != nil {
		r.mu.Unlock()
		return err
	}
	if r.run.CreatedAt.IsZero() {
		r.run.CreatedAt = time.Now()
	}
	r.busy = true
	r.idle = make(chan struct{})
	ctx, r.cancel = context.WithCancel(ctx)
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.busy = false
		r.cancel = nil
		close(r.idle)
		r.mu.Unlock()
	}()

	switch key {
	case PhaseConnect:
		r.execConnect(ctx)
	case PhaseRun:
		r.execRun(ctx)
	case PhaseGenerate:
		r.execGenerate(ctx)
	case PhaseArchive:
		r.execArchive(ctx)
	}
	if ctx.Err() != nil {
		r.markAborted()
		return ctx.Err()
	}
	if r.isAbandoned() {
		return fmt.Errorf("测试已删除")
	}
	return nil
}

// prereqLocked enforces the pipeline order so a later phase cannot be run on an
// empty run (e.g. archiving before anything ran). Callers hold r.mu.
func (r *Runner) prereqLocked(key string) error {
	done := func(k string) bool {
		st := r.phaseStatusLocked(k)
		return st == StatusPassed || st == StatusFailed || st == StatusSkipped || st == StatusAborted
	}
	switch key {
	case PhaseConnect:
		return nil
	case PhaseRun:
		if r.phaseStatusLocked(PhaseConnect) != StatusPassed {
			return fmt.Errorf("请先完成「连接」")
		}
	case PhaseGenerate:
		if !done(PhaseConnect) || !done(PhaseRun) {
			return fmt.Errorf("请先完成「连接」和「运行」")
		}
	case PhaseArchive:
		if !done(PhaseGenerate) {
			return fmt.Errorf("请先完成「生成」")
		}
	}
	return nil
}

func (r *Runner) phaseStatusLocked(key string) string {
	if p := r.run.Phase(key); p != nil {
		return p.Status
	}
	return ""
}

// RunAll drives the whole pipeline: connect, run the checks, generate a report
// and archive. A failed run still gets a report and an archive; a cancelled or
// deleted run stops as soon as the current phase observes it.
func (r *Runner) RunAll(ctx context.Context) {
	if err := r.RunPhase(ctx, PhaseConnect); err != nil {
		if ctx.Err() != nil || r.abortRequestedNow() {
			r.finishAborted()
		}
		return
	}
	if r.isAbandoned() {
		return
	}
	if r.abortRequestedNow() || ctx.Err() != nil {
		r.finishAborted()
		return
	}
	if r.phaseStatus(PhaseConnect) == StatusPassed {
		if err := r.RunPhase(ctx, PhaseRun); err != nil && ctx.Err() == nil && !r.abortRequestedNow() {
			return
		}
	} else {
		r.skipChecks("连接未通过，已跳过运行")
	}
	if r.isAbandoned() {
		return
	}
	if r.abortRequestedNow() || ctx.Err() != nil {
		r.finishAborted()
		return
	}
	if err := r.RunPhase(ctx, PhaseGenerate); err != nil && ctx.Err() == nil && !r.abortRequestedNow() {
		return
	}
	if r.isAbandoned() {
		return
	}
	if r.abortRequestedNow() || ctx.Err() != nil {
		r.finishAborted()
		return
	}
	if err := r.RunPhase(ctx, PhaseArchive); err != nil && ctx.Err() == nil && !r.abortRequestedNow() {
		return
	}
}

// finishAborted records the partial result and writes a final report/archive
// using a fresh context after cancelling the in-flight operation.
func (r *Runner) finishAborted() {
	r.markAborted()
	if r.isAbandoned() {
		return
	}
	if r.phaseStatus(PhaseRun) == StatusPending {
		r.skipChecks("已中止，未运行检查")
	}
	_ = r.RunPhase(context.Background(), PhaseGenerate)
	if !r.isAbandoned() {
		_ = r.RunPhase(context.Background(), PhaseArchive)
	}
}

func (r *Runner) phaseStatus(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.run.Phase(key); p != nil {
		return p.Status
	}
	return ""
}

// beginPhase marks a phase running and publishes it. Only the first phase
// (connect) flips the run status to running; the verdict is set by the run
// phase, and generate/archive never change it.
func (r *Runner) beginPhase(key string) {
	r.mu.Lock()
	if p := r.run.Phase(key); p != nil {
		p.Status = StatusRunning
		p.StartedAt = time.Now()
		p.EndedAt = time.Time{}
	}
	if key == PhaseConnect && r.run.Status == StatusIdle {
		r.run.Status = StatusRunning
	}
	r.mu.Unlock()
	r.update()
}

// endPhase marks a phase finished and publishes it.
func (r *Runner) endPhase(key, status, summary, output, artifact string) {
	r.mu.Lock()
	if p := r.run.Phase(key); p != nil {
		p.Status = status
		p.EndedAt = time.Now()
		p.Summary = summary
		if output != "" {
			p.Output = output
		}
		if artifact != "" {
			p.Artifact = artifact
		}
	}
	r.mu.Unlock()
	r.update()
}

// skipChecks marks the run phase skipped with a reason.
func (r *Runner) skipChecks(reason string) {
	r.mu.Lock()
	if p := r.run.Phase(PhaseRun); p != nil {
		p.Status = StatusSkipped
		p.Summary = reason
	}
	r.mu.Unlock()
	r.update()
}

func (r *Runner) execConnect(ctx context.Context) {
	r.beginPhase(PhaseConnect)
	info := r.dev.Info()
	if !info.Connected {
		r.mu.Lock()
		r.run.Status = StatusFailed
		r.mu.Unlock()
		r.endPhase(PhaseConnect, StatusFailed, fmt.Sprintf("目标会话未连接：%s (%s)", info.Label, info.ID), "", "")
		return
	}
	kind := info.Kind
	if kind == "" {
		kind = "device"
	}
	r.endPhase(PhaseConnect, StatusPassed, fmt.Sprintf("已连接 %s · %s", kind, info.Label), "", "")
}

func (r *Runner) execRun(ctx context.Context) {
	r.beginPhase(PhaseRun)
	if err := r.run.Definition.Validate(); err != nil {
		r.mu.Lock()
		r.run.Status = StatusFailed
		r.mu.Unlock()
		r.endPhase(PhaseRun, StatusFailed, "测试定义无效："+err.Error(), "", "")
		return
	}
	results := r.runChecks(ctx)

	// A cancelled run is aborted, not failed: partial checks must not turn a
	// user abort into a red verdict.
	if ctx.Err() != nil {
		r.mu.Lock()
		if p := r.run.Phase(PhaseRun); p != nil {
			p.Checks = results
		}
		if r.run.Status != StatusPassed && r.run.Status != StatusFailed {
			r.run.Status = StatusAborted
		}
		r.mu.Unlock()
		r.endPhase(PhaseRun, StatusAborted, "已中止", "", "")
		return
	}

	failed := 0
	for _, res := range results {
		if res.Status == CheckFail {
			failed++
		}
	}
	status := StatusPassed
	summary := fmt.Sprintf("%d 项检查全部通过", len(results))
	if failed > 0 {
		status = StatusFailed
		summary = fmt.Sprintf("%d/%d 项检查失败", failed, len(results))
	}
	r.mu.Lock()
	if p := r.run.Phase(PhaseRun); p != nil {
		p.Checks = results
	}
	if failed > 0 {
		r.run.Status = StatusFailed
	} else {
		r.run.Status = StatusPassed
	}
	r.mu.Unlock()
	r.endPhase(PhaseRun, status, summary, "", "")
}

// runChecks executes the case's checks in order. Each check runs a shell command
// or a workspace script, then applies its expectation / exit-code requirement.
func (r *Runner) runChecks(ctx context.Context) []CheckResult {
	checks := r.run.Definition.Checks
	results := make([]CheckResult, 0, len(checks))
	for i, c := range checks {
		if ctx.Err() != nil {
			results = append(results, CheckResult{Index: i, Name: c.Name, Command: checkTarget(c), Status: CheckSkip, Err: "已中止"})
			continue
		}
		results = append(results, r.runCheck(ctx, i, c))
	}
	return results
}

// checkTarget names what a check runs (a script path or a command).
func checkTarget(c Check) string {
	if strings.TrimSpace(c.Script) != "" {
		return c.Script
	}
	return c.Command
}

func (r *Runner) runCheck(ctx context.Context, i int, c Check) CheckResult {
	res := CheckResult{Index: i, Name: c.Name, Command: checkTarget(c), Expect: c.Expect, Status: CheckPass}
	if res.Name == "" && strings.TrimSpace(c.Script) != "" {
		res.Name = path.Base(c.Script)
	}
	timeout := time.Duration(c.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var out string
	switch {
	case strings.TrimSpace(c.Script) != "":
		o, exitOK, err := r.dev.RunScript(cctx, c.Script, timeout)
		out = o
		if err != nil {
			res.Status, res.Err, res.Output = CheckFail, err.Error(), truncate(cleanOutput(out))
			return res
		}
		if c.ExitZero && !exitOK {
			res.Status, res.Err, res.Output = CheckFail, "脚本返回非零退出码", truncate(cleanOutput(out))
			return res
		}
	case strings.TrimSpace(c.Command) != "":
		o, exitOK, err := r.dev.Exec(cctx, c.Command, timeout)
		out = o
		if err != nil {
			res.Status, res.Err, res.Output = CheckFail, err.Error(), truncate(cleanOutput(out))
			return res
		}
		if c.ExitZero && !exitOK {
			res.Status, res.Err, res.Output = CheckFail, "命令返回非零退出码", truncate(cleanOutput(out))
			return res
		}
	}

	if strings.TrimSpace(c.Expect) != "" {
		re, err := regexp.Compile(c.Expect)
		if err != nil {
			res.Status, res.Err, res.Output = CheckFail, "期望值不是合法正则: "+err.Error(), truncate(cleanOutput(out))
			return res
		}
		if c.ExpectNot {
			if re.MatchString(out) {
				res.Status, res.Err, res.Output = CheckFail, "输出不应匹配: "+c.Expect, truncate(cleanOutput(out))
				return res
			}
			if err := r.dev.WaitAbsent(cctx, c.Expect, timeout); err != nil {
				res.Status, res.Err, res.Output = CheckFail, "禁用输出出现或等待被中断: "+c.Expect, truncate(cleanOutput(out))
				return res
			}
		} else if !re.MatchString(out) {
			// The command's own output missed it; wait on the device timeline
			// (streaming consoles) before giving up.
			matched, werr := r.dev.Wait(cctx, c.Expect, timeout)
			if werr != nil {
				res.Status, res.Err, res.Output = CheckFail, "未在超时内匹配: "+c.Expect, truncate(cleanOutput(out))
				return res
			}
			out = matched
		}
	}

	res.Status = CheckPass
	res.Output = truncate(cleanOutput(out))
	return res
}

// cleanOutput makes captured bytes safe to display: ANSI escapes are dropped
// and carriage returns are folded to newlines (serial consoles often redraw a
// line with \r, which otherwise looks like garbage).
func cleanOutput(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimRight(s, "\n")
}

// ansiRe matches CSI/OSC escape sequences.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

func (r *Runner) execGenerate(ctx context.Context) {
	r.beginPhase(PhaseGenerate)
	if ctx.Err() != nil || r.isAbandoned() {
		r.endPhase(PhaseGenerate, StatusSkipped, "已中止", "", "")
		return
	}
	report := r.buildReport()
	r.endPhase(PhaseGenerate, StatusPassed, "已生成测试报告", report, "")
}

func (r *Runner) execArchive(ctx context.Context) {
	r.beginPhase(PhaseArchive)
	// A deleted run must never write its archive (it would reappear on disk).
	if r.isAbandoned() {
		r.endPhase(PhaseArchive, StatusSkipped, "测试已删除，未归档", "", "")
		return
	}
	if r.store == nil {
		r.endPhase(PhaseArchive, StatusSkipped, "未配置归档器", "", "")
		return
	}
	r.mu.Lock()
	snap := r.snapshotLocked()
	r.mu.Unlock()
	path, err := r.store.Archive(&snap)
	if err != nil {
		r.endPhase(PhaseArchive, StatusFailed, "归档失败："+err.Error(), "", "")
		return
	}
	r.mu.Lock()
	r.run.ArchivedPath = path
	r.run.EndedAt = time.Now()
	r.mu.Unlock()
	r.endPhase(PhaseArchive, StatusPassed, "已归档到工作区 "+path, "", path)
}

// buildReport renders the generated report (Markdown).
func (r *Runner) buildReport() string {
	r.mu.Lock()
	run := r.snapshotLocked()
	r.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "# 测试报告：%s\n\n", run.Name)
	fmt.Fprintf(&b, "- 运行 ID：`%s`\n", run.ID)
	fmt.Fprintf(&b, "- 目标会话：`%s`\n", run.SessionID)
	fmt.Fprintf(&b, "- 时间：%s\n", run.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- 结论：**%s**\n", run.ResultLabel())
	if run.DefinitionRef != "" {
		fmt.Fprintf(&b, "- 定义来源：`%s`\n", run.DefinitionRef)
	}

	r.mu.Lock()
	prev := r.prev
	r.mu.Unlock()
	if prev != nil {
		fmt.Fprintf(&b, "- 上次（%s）：%s\n", prev.CreatedAt.Format("2006-01-02 15:04"), prev.ResultLabel())
		switch {
		case prev.Status == StatusPassed && run.Status == StatusFailed:
			b.WriteString("- **回归**：上次通过，本次失败\n")
		case prev.Status == StatusFailed && run.Status == StatusPassed:
			b.WriteString("- 修复：上次失败，本次通过\n")
		}
	}

	if p := run.Phase(PhaseRun); p != nil && len(p.Checks) > 0 {
		b.WriteString("\n## 检查项\n\n")
		b.WriteString("| # | 检查 | 命令 | 结果 | 说明 |\n| --- | --- | --- | --- | --- |\n")
		for _, c := range p.Checks {
			name := c.Name
			if name == "" {
				name = fmt.Sprintf("check-%d", c.Index+1)
			}
			note := c.Err
			if note == "" {
				note = "ok"
			}
			fmt.Fprintf(&b, "| %d | %s | `%s` | %s | %s |\n",
				c.Index+1, mdCell(name), mdCell(c.Command), checkLabel(c.Status), mdCell(note))
		}
	} else {
		b.WriteString("\n（没有检查项）\n")
	}
	return b.String()
}

// ResultLabel is a short Chinese verdict for the run.
func (r *Run) ResultLabel() string {
	switch r.Status {
	case StatusPassed:
		return "通过"
	case StatusFailed:
		return "失败"
	case StatusAborted:
		return "已中止"
	case StatusRunning:
		return "进行中"
	default:
		return "未开始"
	}
}

func checkLabel(status string) string {
	switch status {
	case CheckPass:
		return "✅ 通过"
	case CheckFail:
		return "❌ 失败"
	case CheckSkip:
		return "⏭ 跳过"
	default:
		return status
	}
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string) string {
	const max = 8192
	if len(s) > max {
		return s[:max] + "\n…(已截断)"
	}
	return strings.TrimRight(s, "\n")
}
