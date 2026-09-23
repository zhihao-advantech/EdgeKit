// Package agent implements EdgeKit's built-in agent: a natural-language
// assistant that drives connected devices through the tools contributed by the
// installed kits.
//
// The agent does not define tools itself — it consumes a kit.Registry, so a
// capability is declared once (in a kit) and shared by the agent, the UI
// protocol and, later, an MCP server.
//
// With an OpenAI-compatible model configured it runs a function-calling loop;
// without one it falls back to deterministic built-in workflows (inspection,
// logs, disk, memory, ping, ...). Mutating tools require user approval unless
// auto-run is enabled.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"edgekit/internal/kit"
	"edgekit/internal/workspace"
)

// Event kinds.
const (
	KindUser      = "user"
	KindAssistant = "assistant"
	KindTool      = "tool"
	KindApproval  = "approval"
	KindStatus    = "status"
	KindError     = "error"
	KindDone      = "done"
)

// Event is one item in the agent transcript.
type Event struct {
	Kind   string    `json:"kind"`
	ID     string    `json:"id,omitempty"`
	Text   string    `json:"text,omitempty"`
	Tool   string    `json:"tool,omitempty"`
	Args   string    `json:"args,omitempty"`
	Result string    `json:"result,omitempty"`
	State  string    `json:"state,omitempty"`
	Time   time.Time `json:"time"`
}

// Target values: where a command should run.
const (
	TargetRemote = "remote" // a device reached over SSH / serial
	TargetLocal  = "local"  // the machine running EdgeKit
)

// Config configures the model backend and the default target.
type Config struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
	AutoRun bool   `json:"autoRun"`
	Target  string `json:"target"` // "remote" (default) or "local"
}

// Deps and the capability interfaces are aliases of the kit package, so the
// host implements them once for the kits and the agent alike.
type (
	Deps         = kit.Deps
	SerialAccess = kit.Serial
	SSHAccess    = kit.SSH
	SFTPAccess   = kit.SFTP
)

// Target returns the currently selected target (defaults to Remote). The old
// "edge"/"host" values are still accepted for backward compatibility.
func (m *Manager) Target() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.cfg.Target {
	case TargetLocal, "host":
		return TargetLocal
	default:
		return TargetRemote
	}
}

func targetLabel(target string) string {
	if target == TargetLocal {
		return "Local（本机）"
	}
	return "Remote（远端设备）"
}

// Manager runs agent turns against the tools in a kit registry.
type Manager struct {
	mu       sync.Mutex
	cfg      Config
	deps     Deps
	registry *kit.Registry
	history  []chatMessage
	pending  map[string]chan bool
	seq      int
	cancel   context.CancelFunc
	running  bool
	onEvent  func(Event)
}

// New builds an agent manager backed by reg. deps is used to describe the
// current environment in the system prompt.
func New(reg *kit.Registry, deps Deps, onEvent func(Event)) *Manager {
	if reg == nil {
		reg = kit.NewRegistry()
	}
	return &Manager{
		deps:     deps,
		registry: reg,
		pending:  make(map[string]chan bool),
		onEvent:  onEvent,
	}
}

// Config returns the current model configuration.
func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// SetConfig updates the model configuration.
func (m *Manager) SetConfig(cfg Config) {
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
}

// Reset clears the conversation.
func (m *Manager) Reset() {
	m.mu.Lock()
	m.history = nil
	m.mu.Unlock()
	m.emit(Event{Kind: KindStatus, Text: "对话已清空"})
}

// Cancel aborts the running turn.
func (m *Manager) Cancel() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Approve answers a pending approval request.
func (m *Manager) Approve(id string, allow bool) {
	m.mu.Lock()
	ch := m.pending[id]
	delete(m.pending, id)
	m.mu.Unlock()
	if ch != nil {
		select {
		case ch <- allow:
		default:
		}
	}
}

// Send starts a new turn.
func (m *Manager) Send(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		m.emit(Event{Kind: KindError, Text: "上一条指令仍在执行，请稍候或点击「停止」"})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.running = true
	cfg := m.cfg
	m.mu.Unlock()

	m.emit(Event{Kind: KindUser, Text: text})
	m.appendMessage(chatMessage{Role: "user", Content: text})

	go func() {
		defer func() {
			m.mu.Lock()
			m.running = false
			m.cancel = nil
			m.mu.Unlock()
			m.emit(Event{Kind: KindDone})
		}()
		if cfg.APIKey != "" && cfg.Model != "" {
			m.runLLM(ctx)
		} else {
			m.runLocal(ctx, text)
		}
	}()
}

func (m *Manager) emit(ev Event) {
	ev.Time = time.Now()
	if m.onEvent != nil {
		m.onEvent(ev)
	}
}

func (m *Manager) appendMessage(msg chatMessage) {
	m.mu.Lock()
	m.history = append(m.history, msg)
	if len(m.history) > 40 {
		m.history = m.history[len(m.history)-40:]
	}
	m.mu.Unlock()
}

func (m *Manager) snapshot(system string) []chatMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]chatMessage, 0, len(m.history)+1)
	out = append(out, chatMessage{Role: "system", Content: system})
	out = append(out, m.history...)
	return out
}

func (m *Manager) autoRun() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.AutoRun
}

func (m *Manager) nextID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	return fmt.Sprintf("t%d", m.seq)
}

// runTool executes a registry tool, asking for approval when required.
func (m *Manager) runTool(ctx context.Context, name string, args map[string]any, force bool) (string, error) {
	t, ok := m.registry.Tool(name)
	if !ok {
		return "", fmt.Errorf("未知工具: %s", name)
	}
	argText := marshalArgs(args)

	if t.Mutating() && !force && !m.autoRun() {
		if !m.requestApproval(ctx, name, argText) {
			m.emit(Event{Kind: KindTool, Tool: name, Args: argText, State: "denied", Result: "用户拒绝执行"})
			return "用户拒绝执行该操作", nil
		}
	}

	m.emit(Event{Kind: KindTool, Tool: name, Args: argText, State: "running"})
	out, err := t.Call(ctx, args)
	state := "ok"
	if err != nil {
		state = "error"
		out = err.Error()
	}
	m.emit(Event{Kind: KindTool, Tool: name, Args: argText, State: state, Result: kit.Truncate(out, 4000)})
	return out, err
}

func (m *Manager) requestApproval(ctx context.Context, toolName, argText string) bool {
	id := m.nextID()
	ch := make(chan bool, 1)
	m.mu.Lock()
	m.pending[id] = ch
	m.mu.Unlock()

	m.emit(Event{Kind: KindApproval, ID: id, Tool: toolName, Args: argText, State: "pending"})

	select {
	case allow := <-ch:
		return allow
	case <-time.After(5 * time.Minute):
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
		return false
	case <-ctx.Done():
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
		return false
	}
}

/* ------------------------------------------------------------------ *
 * built-in workflows (no model configured)
 * ------------------------------------------------------------------ */

// remoteExecTool picks the best transport to run a command on the remote device:
// SSH when connected, otherwise the serial console.
func (m *Manager) remoteExecTool() string {
	if m.deps.SSH != nil && m.deps.SSH.IsConnected() {
		return "ssh_exec"
	}
	if m.deps.Serial != nil && m.deps.Serial.IsOpen() {
		return "serial_exec"
	}
	return ""
}

func (m *Manager) runLocal(ctx context.Context, text string) {
	lower := strings.ToLower(text)
	target := m.Target()
	m.emit(Event{Kind: KindStatus, Text: "Normal 模式（未配置模型），目标 " + targetLabel(target)})
	switch {
	case hasAny(lower, "巡检", "体检", "检查", "状态", "inspect", "check", "status"):
		m.localInspect(ctx, target)
	case hasAny(lower, "日志", "log", "dmesg", "journal"):
		m.localRun(ctx, target, "系统日志", "dmesg 2>/dev/null | tail -n 50 || journalctl -n 50 --no-pager")
	case hasAny(lower, "磁盘", "空间", "df"):
		m.localRun(ctx, target, "磁盘使用", "df -h")
	case hasAny(lower, "内存", "free", "mem"):
		m.localRun(ctx, target, "内存使用", "free -m")
	case hasAny(lower, "进程", "process", "ps", "top"):
		m.localRun(ctx, target, "进程负载", "ps -eo pid,comm,%cpu,%mem --sort=-%cpu | head -n 15")
	case hasAny(lower, "版本", "系统", "uname", "os", "内核"):
		m.localRun(ctx, target, "系统版本", "uname -a; (cat /etc/os-release 2>/dev/null | head -n 5)")
	case hasAny(lower, "ping", "网络", "连通", "network"):
		m.localPing(ctx, text)
	default:
		m.emit(Event{Kind: KindAssistant, Text: localHelp()})
	}
}

func (m *Manager) localInspect(ctx context.Context, target string) {
	m.emit(Event{Kind: KindAssistant, Text: "开始巡检（目标 " + targetLabel(target) + "）…"})
	if _, err := m.runTool(ctx, "local_info", nil, true); err != nil {
		m.emit(Event{Kind: KindError, Text: err.Error()})
	}

	const summary = "uname -a; uptime; echo '--- disk ---'; df -h; echo '--- mem ---'; free -m"
	if target == TargetLocal {
		m.emit(Event{Kind: KindAssistant, Text: "检查本机（Local）状态…"})
		if _, err := m.runTool(ctx, "local_exec", map[string]any{"command": summary}, true); err != nil {
			m.emit(Event{Kind: KindError, Text: err.Error()})
		}
	} else if tool := m.remoteExecTool(); tool != "" {
		via := "SSH"
		if tool == "serial_exec" {
			via = "串口"
		}
		m.emit(Event{Kind: KindAssistant, Text: "通过" + via + "检查远端设备状态…"})
		if _, err := m.runTool(ctx, tool, map[string]any{"command": summary}, true); err != nil {
			m.emit(Event{Kind: KindError, Text: err.Error()})
		}
	} else {
		m.emit(Event{Kind: KindAssistant, Text: "远端未连接，跳过设备检查。可连接 SSH 或打开串口后重试。"})
	}

	if m.deps.Serial != nil && m.deps.Serial.IsOpen() {
		m.emit(Event{Kind: KindAssistant, Text: "串口 " + m.deps.Serial.Port() + " 已打开，读取最近输出…"})
		m.runTool(ctx, "serial_read", nil, true)
	}
	m.emit(Event{Kind: KindAssistant, Text: "巡检完成。"})
}

// localRun executes a built-in read-only command on the selected target.
func (m *Manager) localRun(ctx context.Context, target, label, cmd string) {
	if target == TargetLocal {
		m.emit(Event{Kind: KindAssistant, Text: "在本机（Local）读取" + label + "…"})
		if _, err := m.runTool(ctx, "local_exec", map[string]any{"command": cmd}, true); err != nil {
			m.emit(Event{Kind: KindError, Text: err.Error()})
		}
		return
	}
	tool := m.remoteExecTool()
	if tool == "" {
		m.emit(Event{Kind: KindAssistant, Text: "远端未连接：请先连接 SSH 会话，或打开串口后重试。"})
		return
	}
	m.emit(Event{Kind: KindAssistant, Text: "在远端设备上读取" + label + "…"})
	if _, err := m.runTool(ctx, tool, map[string]any{"command": cmd}, true); err != nil {
		m.emit(Event{Kind: KindError, Text: err.Error()})
	}
}

var hostRe = regexp.MustCompile(`(?i)\b((?:\d{1,3}\.){3}\d{1,3}|localhost|[a-z0-9][a-z0-9.-]*\.[a-z]{2,})\b`)

func (m *Manager) localPing(ctx context.Context, text string) {
	host := ""
	if mm := hostRe.FindString(text); mm != "" {
		host = mm
	} else if m.deps.SSH != nil && m.deps.SSH.IsConnected() {
		if t := m.deps.SSH.Target(); t != "" {
			host = strings.SplitN(strings.SplitN(t, "@", 2)[1], ":", 2)[0]
		}
	}
	if host == "" {
		m.emit(Event{Kind: KindAssistant, Text: "请提供要测试的主机，例如：ping 192.0.2.10"})
		return
	}
	m.emit(Event{Kind: KindAssistant, Text: "检测 " + host + " 的连通性…"})
	m.runTool(ctx, "net_ping", map[string]any{"host": host, "count": 4}, true)
	m.runTool(ctx, "net_check_port", map[string]any{"host": host, "port": 22}, true)
}

func localHelp() string {
	return strings.Join([]string{
		"当前为 Normal 模式（未配置 AI 模型），使用内置流程。可直接说：",
		"· 巡检 / 检查设备状态",
		"· 查看日志 / 磁盘 / 内存 / 进程 / 系统版本",
		"· ping 192.0.2.10（从本机检测连通性与端口）",
		"",
		"这些操作会作用于当前「目标」：Local（本机）或 Remote（远端设备），",
		"可在「会话信息」面板切换目标。",
		"",
		"在左侧「会话设置 → Agent」中填入 OpenAI 兼容的 Base URL、API Key 与模型名后，",
		"即可用自然语言驱动串口 / SSH / 工作区完成更复杂的任务。",
	}, "\n")
}

func hasAny(s string, keys ...string) bool {
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func marshalArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(b)
}

// workspaceRoot is kept for the system prompt.
func workspaceRoot() string {
	root, err := workspace.Root()
	if err != nil {
		return ""
	}
	return root
}
