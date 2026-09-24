package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"edgekit/internal/acp"
	"edgekit/internal/kit"
	"edgekit/internal/workspace"
)

// acpBackend drives an external agent (Hermes, OpenClaw, ...) over the Agent
// Client Protocol. The agent is a child process; EdgeKit's tools reach it
// through the `edgekit mcp` stdio server handed over at session creation, so
// the kit registry and the approval gate stay the single source of truth.
type acpBackend struct {
	cfg Config
	m   *Manager

	mu        sync.Mutex
	client    *acp.Client
	sessionID string
	buf       strings.Builder
	toolName  map[string]string // toolCallId -> display name
	toolArgs  map[string]string // toolCallId -> argument text

	models       []acp.ModelInfo
	currentModel string
}

func newACPBackend(cfg Config, m *Manager) *acpBackend {
	return &acpBackend{
		cfg:      cfg,
		m:        m,
		toolName: make(map[string]string),
		toolArgs: make(map[string]string),
	}
}

func (b *acpBackend) Send(ctx context.Context, text string) error {
	b.mu.Lock()
	needStart := !(b.client != nil && b.client.Alive())
	b.mu.Unlock()
	if needStart {
		b.m.emit(Event{Kind: KindStatus, Text: "正在启动外部 Agent…"})
	}
	if err := b.ensureProcess(ctx); err != nil {
		return err
	}
	if err := b.ensureSession(ctx); err != nil {
		return err
	}
	b.m.emit(Event{Kind: KindStatus, Text: b.label()})

	b.mu.Lock()
	c := b.client
	sid := b.sessionID
	b.mu.Unlock()
	if c == nil {
		return errors.New("ACP Agent 未就绪")
	}

	_, err := c.Prompt(ctx, sid, text)
	b.flush()
	if err != nil {
		if ctx.Err() != nil {
			return nil // cancelled by the user
		}
		return fmt.Errorf("ACP: %w", err)
	}
	return nil
}

func (b *acpBackend) Cancel() {
	b.mu.Lock()
	c := b.client
	sid := b.sessionID
	b.mu.Unlock()
	if c == nil || sid == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.Cancel(ctx, sid)
}

func (b *acpBackend) Reset() {
	b.flush()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.NewSession(ctx); err != nil {
		b.m.emit(Event{Kind: KindError, Text: "重置 ACP 会话失败: " + err.Error()})
	}
}

func (b *acpBackend) Close() error {
	b.mu.Lock()
	c := b.client
	b.client = nil
	b.sessionID = ""
	b.models = nil
	b.currentModel = ""
	b.mu.Unlock()
	if c != nil {
		return c.Close()
	}
	return nil
}

// NewSession opens a fresh conversation on the running process (starting it if
// needed). The process itself is kept alive.
func (b *acpBackend) NewSession(ctx context.Context) error {
	if err := b.ensureProcess(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	c := b.client
	b.mu.Unlock()
	if c == nil {
		return errors.New("ACP Agent 未就绪")
	}
	sess, err := c.NewSession(ctx, b.workDir(), b.mcpServers())
	if err != nil {
		return fmt.Errorf("创建 ACP 会话失败: %w", err)
	}
	b.applySession(ctx, c, sess)
	return nil
}

// ListSessions returns the agent's persisted conversations.
func (b *acpBackend) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	if err := b.ensureProcess(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	c := b.client
	b.mu.Unlock()
	if c == nil {
		return nil, errors.New("ACP Agent 未就绪")
	}
	infos, err := c.ListSessions(ctx, b.workDir())
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, 0, len(infos))
	for _, s := range infos {
		out = append(out, SessionInfo{ID: s.SessionID, CWD: s.CWD, Title: s.Title, UpdatedAt: s.UpdatedAt})
	}
	return out, nil
}

// LoadSession resumes a persisted conversation on the running process.
func (b *acpBackend) LoadSession(ctx context.Context, sessionID string) error {
	if err := b.ensureProcess(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	c := b.client
	b.mu.Unlock()
	if c == nil {
		return errors.New("ACP Agent 未就绪")
	}
	sess, err := c.LoadSession(ctx, b.workDir(), sessionID, b.mcpServers())
	if err != nil {
		return fmt.Errorf("恢复 ACP 会话失败: %w", err)
	}
	b.applySession(ctx, c, sess)
	return nil
}

// CurrentSession returns the active ACP session id ("" when none).
func (b *acpBackend) CurrentSession() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionID
}

// applySession records a newly created/loaded session and applies the desired
// model.
func (b *acpBackend) applySession(ctx context.Context, c *acp.Client, sess acp.Session) {
	current := sess.CurrentModelID
	if want := b.desiredModel(); want != "" && want != current {
		if err := c.SetModel(ctx, sess.ID, want); err != nil {
			log.Printf("[acp] 切换模型 %s 失败: %v", want, err)
		} else {
			current = want
		}
	}
	b.mu.Lock()
	b.sessionID = sess.ID
	b.models = sess.Models
	b.currentModel = current
	b.buf.Reset()
	b.toolName = make(map[string]string)
	b.toolArgs = make(map[string]string)
	b.mu.Unlock()
}

// ensureProcess starts the child process (and performs the ACP handshake) if it
// is not already running. It never touches the session.
func (b *acpBackend) ensureProcess(ctx context.Context) error {
	b.mu.Lock()
	if b.client != nil && b.client.Alive() {
		b.mu.Unlock()
		return nil
	}
	stale := b.client
	b.client = nil
	b.mu.Unlock()
	if stale != nil {
		_ = stale.Close()
	}

	client := acp.New(acp.Options{
		Command:      b.cfg.ACPCommand,
		Args:         b.args(),
		Env:          b.modelEnv(),
		Dir:          b.workDir(),
		OnUpdate:     b.onUpdate,
		OnPermission: b.onPermission,
		OnExit:       b.onExit,
		Logf:         func(f string, a ...any) { log.Printf("[acp] "+f, a...) },
	})
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("启动 ACP Agent 失败: %w", err)
	}
	b.mu.Lock()
	b.client = client
	b.mu.Unlock()
	return nil
}

// ensureSession creates a session on the running process if none is active.
func (b *acpBackend) ensureSession(ctx context.Context) error {
	b.mu.Lock()
	ready := b.client != nil && b.client.Alive() && b.sessionID != ""
	b.mu.Unlock()
	if ready {
		return nil
	}
	return b.NewSession(ctx)
}

// ensure starts the process and creates a session (used by Prepare).
func (b *acpBackend) ensure(ctx context.Context) error {
	if err := b.ensureProcess(ctx); err != nil {
		return err
	}
	return b.ensureSession(ctx)
}

// Prepare starts the agent (if needed) so its model list is known.
func (b *acpBackend) Prepare(ctx context.Context) error { return b.ensure(ctx) }

// Models returns the agent's selectable models, with the EdgeKit override (if
// any) listed first.
func (b *acpBackend) Models() []Model {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Model, 0, len(b.models)+1)
	seen := map[string]bool{}
	if b.cfg.ACPOverride && b.cfg.Model != "" {
		id := encodeModel(b.cfg.ACPCommand, b.cfg.Model)
		out = append(out, Model{ID: id, Name: b.cfg.Model, Description: "EdgeKit 覆盖"})
		seen[id] = true
	}
	for _, m := range b.models {
		if m.ModelID == "" || seen[m.ModelID] {
			continue
		}
		out = append(out, Model{ID: m.ModelID, Name: m.Name, Description: m.Description})
		seen[m.ModelID] = true
	}
	return out
}

// CurrentModel returns the active model id.
func (b *acpBackend) CurrentModel() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.currentModel != "" {
		return b.currentModel
	}
	return b.cfg.ACPModel
}

// SetModel switches the model on the live session.
func (b *acpBackend) SetModel(ctx context.Context, modelID string) error {
	if err := b.ensureSession(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	c := b.client
	sid := b.sessionID
	b.mu.Unlock()
	if c == nil {
		return errors.New("ACP Agent 未就绪")
	}
	if err := c.SetModel(ctx, sid, modelID); err != nil {
		return err
	}
	b.mu.Lock()
	b.currentModel = modelID
	b.mu.Unlock()
	return nil
}

// desiredModel is the model to apply when a session is created: the EdgeKit
// override wins, otherwise the last model the user picked.
func (b *acpBackend) desiredModel() string {
	if b.cfg.ACPOverride && b.cfg.Model != "" {
		return encodeModel(b.cfg.ACPCommand, b.cfg.Model)
	}
	return b.cfg.ACPModel
}

// modelEnv maps EdgeKit's model fields onto the agent's environment so a
// reachable endpoint can be supplied from the UI. Currently Hermes only.
func (b *acpBackend) modelEnv() []string {
	if !b.cfg.ACPOverride {
		return nil
	}
	switch strings.ToLower(filepath.Base(b.cfg.ACPCommand)) {
	case "hermes":
		var env []string
		if b.cfg.BaseURL != "" {
			env = append(env, "CUSTOM_BASE_URL="+b.cfg.BaseURL)
		}
		if b.cfg.APIKey != "" {
			env = append(env, "CUSTOM_API_KEY="+b.cfg.APIKey)
		}
		return env
	}
	return nil
}

// encodeModel qualifies a bare model name with the provider Hermes needs
// ("custom:<model>"), so switching keeps the custom endpoint.
func encodeModel(command, model string) string {
	if model == "" || strings.Contains(model, ":") {
		return model
	}
	if strings.EqualFold(filepath.Base(command), "hermes") {
		return "custom:" + model
	}
	return model
}

func (b *acpBackend) args() []string {
	if len(b.cfg.ACPArgs) > 0 {
		return b.cfg.ACPArgs
	}
	return []string{"acp"}
}

func (b *acpBackend) workDir() string {
	if root, err := workspace.Root(); err == nil && root != "" {
		return root
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return os.TempDir()
}

// mcpServers hands the agent EdgeKit's own tool surface. The MCP bridge talks
// back to this running instance, so the agent acts on the devices the user has
// already connected and approvals surface in the EdgeKit UI.
func (b *acpBackend) mcpServers() []acp.MCPServer {
	cmd := os.Getenv("EDGEKIT_MCP_COMMAND")
	if cmd == "" {
		exe, err := os.Executable()
		if err != nil || exe == "" {
			return nil
		}
		cmd = exe
	}
	return []acp.MCPServer{{Name: "edgekit", Command: cmd, Args: []string{"mcp"}}}
}

func (b *acpBackend) label() string {
	name := filepath.Base(b.cfg.ACPCommand)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "ACP"
	}
	return "ACP 模式（" + name + "）"
}

/* ------------------------------------------------------------------ *
 * ACP → agent.Event mapping
 * ------------------------------------------------------------------ */

func (b *acpBackend) onUpdate(u acp.Update) {
	switch u.SessionUpdate {
	case acp.UpdateAgentMessage:
		if t := u.Text(); t != "" {
			b.appendText(t)
		}
	case acp.UpdateToolCall:
		b.flush()
		b.recordTool(u)
		b.emitTool(u, "running", "")
	case acp.UpdateToolCallUpdate:
		b.emitTool(u, toolState(u.Status), b.toolResult(u))
	}
}

func (b *acpBackend) appendText(t string) {
	b.mu.Lock()
	b.buf.WriteString(t)
	b.mu.Unlock()
}

func (b *acpBackend) flush() {
	b.mu.Lock()
	text := b.buf.String()
	b.buf.Reset()
	b.mu.Unlock()
	if strings.TrimSpace(text) != "" {
		b.m.emit(Event{Kind: KindAssistant, Text: text})
	}
}

func (b *acpBackend) recordTool(u acp.Update) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if u.Title != "" {
		b.toolName[u.ToolCallID] = u.Title
	}
	if a := b.argsText(u); a != "" {
		b.toolArgs[u.ToolCallID] = a
	}
}

func (b *acpBackend) emitTool(u acp.Update, state, result string) {
	b.mu.Lock()
	tool := b.toolName[u.ToolCallID]
	args := b.toolArgs[u.ToolCallID]
	b.mu.Unlock()
	if tool == "" {
		tool = u.Title
	}
	if tool == "" {
		tool = "tool"
	}
	if args == "" {
		args = b.argsText(u)
	}
	b.m.emit(Event{
		Kind:   KindTool,
		Tool:   tool,
		Args:   args,
		State:  state,
		Result: kit.Truncate(result, 4000),
	})
}

func (b *acpBackend) argsText(u acp.Update) string {
	if len(u.RawInput) > 0 && string(u.RawInput) != "null" {
		return kit.Truncate(string(u.RawInput), 500)
	}
	if t := u.ContentText(); t != "" {
		return kit.Truncate(t, 500)
	}
	return ""
}

func (b *acpBackend) toolResult(u acp.Update) string {
	if len(u.RawOutput) > 0 && string(u.RawOutput) != "null" {
		return string(u.RawOutput)
	}
	return u.ContentText()
}

func toolState(status string) string {
	switch status {
	case "completed":
		return "ok"
	case "failed":
		return "error"
	default:
		return "running"
	}
}

// onPermission routes an ACP approval request through the same policy gate the
// built-in agent and the MCP surface use, so the UI prompt (and auto-run) are
// identical.
func (b *acpBackend) onPermission(ctx context.Context, req acp.PermissionRequest) acp.PermissionOutcome {
	tool := req.ToolCall.Title
	if tool == "" {
		tool = req.ToolCall.Kind
	}
	if tool == "" {
		tool = "tool"
	}
	args := ""
	if len(req.ToolCall.RawInput) > 0 {
		args = string(req.ToolCall.RawInput)
	}

	allow := true
	if b.m.gate != nil {
		if err := b.m.gate.Check(ctx, tool, kit.RiskMutate, args); err != nil {
			allow = false
		}
	}
	if allow {
		if opt := pickOption(req.Options, "allow"); opt != "" {
			return acp.Selected(opt)
		}
		return acp.Selected("")
	}
	if opt := pickOption(req.Options, "reject"); opt != "" {
		return acp.Selected(opt)
	}
	return acp.Cancelled()
}

func (b *acpBackend) onExit(err error) {
	b.mu.Lock()
	b.client = nil
	b.sessionID = ""
	b.mu.Unlock()
	if err != nil && !errors.Is(err, acp.ErrClosed) {
		b.m.emit(Event{Kind: KindError, Text: "Agent 进程退出: " + err.Error()})
	}
}

func pickOption(opts []acp.PermissionOption, want string) string {
	for _, o := range opts {
		if strings.HasPrefix(o.Kind, want) {
			return o.OptionID
		}
	}
	return ""
}
