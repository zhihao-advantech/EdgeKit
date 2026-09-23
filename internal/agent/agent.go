// Package agent implements the EdgeKit AI agent: a natural-language assistant
// that drives the serial / SSH / SFTP sessions through a tool interface.
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
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"path/filepath"

	"edgekit/internal/netdiag"
	"edgekit/internal/sftpx"
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

// SerialAccess is the serial capability the agent needs.
type SerialAccess interface {
	IsOpen() bool
	Port() string
	Write(p []byte) error
	Recent() []byte
	RunCapture(command string, quiet, timeout time.Duration) (string, error)
}

// SSHAccess is the SSH capability the agent needs.
type SSHAccess interface {
	IsConnected() bool
	Target() string
	ExecCapture(command string, maxBytes int) (string, error)
}

// SFTPAccess is the SFTP capability the agent needs.
type SFTPAccess interface {
	IsConnected() bool
	List(path string) ([]sftpx.Entry, error)
	Download(path string) ([]byte, error)
	Upload(path string, data []byte) error
}

// Deps bundles the backend capabilities.
type Deps struct {
	Serial SerialAccess
	SSH    SSHAccess
	SFTP   SFTPAccess
}

type tool struct {
	name        string
	description string
	mutating    bool
	schema      map[string]any
	run         func(ctx context.Context, args map[string]any) (string, error)
}

// Manager runs agent turns.
type Manager struct {
	mu      sync.Mutex
	cfg     Config
	deps    Deps
	history []chatMessage
	tools   []*tool
	byName  map[string]*tool
	pending map[string]chan bool
	seq     int
	cancel  context.CancelFunc
	running bool
	onEvent func(Event)
}

// New builds an agent manager.
func New(deps Deps, onEvent func(Event)) *Manager {
	m := &Manager{
		deps:    deps,
		pending: make(map[string]chan bool),
		byName:  make(map[string]*tool),
		onEvent: onEvent,
	}
	m.registerTools()
	return m
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

// runTool executes a tool, asking for approval when required.
func (m *Manager) runTool(ctx context.Context, name string, args map[string]any, force bool) (string, error) {
	m.mu.Lock()
	t := m.byName[name]
	m.mu.Unlock()
	if t == nil {
		return "", fmt.Errorf("未知工具: %s", name)
	}
	argText := marshalArgs(args)

	if t.mutating && !force && !m.autoRun() {
		if !m.requestApproval(ctx, t, argText) {
			m.emit(Event{Kind: KindTool, Tool: name, Args: argText, State: "denied", Result: "用户拒绝执行"})
			return "用户拒绝执行该操作", nil
		}
	}

	m.emit(Event{Kind: KindTool, Tool: name, Args: argText, State: "running"})
	out, err := t.run(ctx, args)
	state := "ok"
	if err != nil {
		state = "error"
		out = err.Error()
	}
	m.emit(Event{Kind: KindTool, Tool: name, Args: argText, State: state, Result: truncate(out, 4000)})
	return out, err
}

func (m *Manager) requestApproval(ctx context.Context, t *tool, argText string) bool {
	id := m.nextID()
	ch := make(chan bool, 1)
	m.mu.Lock()
	m.pending[id] = ch
	m.mu.Unlock()

	m.emit(Event{Kind: KindApproval, ID: id, Tool: t.name, Args: argText, State: "pending"})

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
		"可在 Agent 标题栏切换目标。",
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

// hostExec runs a command on the local machine (Local target).
func hostExec(ctx context.Context, command string, maxBytes int) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("命令为空")
	}
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	buf := &capBuffer{max: maxBytes}
	cmd.Stdout = buf
	cmd.Stderr = buf
	runErr := cmd.Run()
	out := buf.String()
	if runErr != nil {
		if out == "" {
			return "", fmt.Errorf("命令执行失败: %w", runErr)
		}
		out += "\n(exit: " + runErr.Error() + ")"
	}
	return out, nil
}

// capBuffer collects at most max bytes.
type capBuffer struct {
	max       int
	buf       []byte
	truncated bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if room := b.max - len(b.buf); room > 0 {
		n := room
		if n > len(p) {
			n = len(p)
		}
		b.buf = append(b.buf, p[:n]...)
		if n < len(p) {
			b.truncated = true
		}
	} else {
		b.truncated = true
	}
	return len(p), nil
}

func (b *capBuffer) String() string {
	s := string(b.buf)
	if b.truncated {
		s += "\n…(输出已截断)"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(已截断)"
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

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func argInt(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

/* ------------------------------------------------------------------ *
 * tool registry
 * ------------------------------------------------------------------ */
func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func sType() map[string]any { return map[string]any{"type": "string"} }
func iType() map[string]any { return map[string]any{"type": "integer"} }

func (m *Manager) registerTools() {
	m.tools = []*tool{
		{
			name:        "local_info",
			description: "获取 Local（运行 EdgeKit 的本机）的主机名、系统、CPU 等信息",
			schema:      obj(nil),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				return netdiag.SysInfo(), nil
			},
		},
		{
			name:        "local_exec",
			description: "在 Local（运行 EdgeKit 的本机）上执行 shell 命令并返回输出",
			mutating:    true,
			schema:      obj(map[string]any{"command": sType()}, "command"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				return hostExec(ctx, argString(args, "command"), 64*1024)
			},
		},
		{
			name:        "net_ping",
			description: "对目标主机执行 ICMP ping，返回连通性与延迟",
			schema:      obj(map[string]any{"host": sType(), "count": iType()}, "host"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				return netdiag.Ping(ctx, argString(args, "host"), argInt(args, "count", 4))
			},
		},
		{
			name:        "net_check_port",
			description: "检查目标主机某个 TCP 端口是否开放",
			schema:      obj(map[string]any{"host": sType(), "port": iType()}, "host", "port"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				host := argString(args, "host")
				port := argInt(args, "port", 22)
				open, rtt, err := netdiag.CheckPort(host, port)
				if err != nil {
					return "", err
				}
				if open {
					return fmt.Sprintf("%s:%d 开放，耗时 %d ms", host, port, rtt.Milliseconds()), nil
				}
				return fmt.Sprintf("%s:%d 不可达", host, port), nil
			},
		},
		{
			name:        "net_resolve",
			description: "解析域名对应的 IP 地址",
			schema:      obj(map[string]any{"host": sType()}, "host"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				ips, err := netdiag.Resolve(argString(args, "host"))
				if err != nil {
					return "", err
				}
				return strings.Join(ips, "\n"), nil
			},
		},
		{
			name:        "serial_status",
			description: "查询串口会话是否已打开及其参数",
			schema:      obj(nil),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				if m.deps.Serial == nil || !m.deps.Serial.IsOpen() {
					return "串口未打开", nil
				}
				return "串口已打开: " + m.deps.Serial.Port(), nil
			},
		},
		{
			name:        "serial_read",
			description: "读取串口最近接收到的数据（文本）",
			schema:      obj(nil),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				if m.deps.Serial == nil || !m.deps.Serial.IsOpen() {
					return "", fmt.Errorf("串口未打开")
				}
				data := m.deps.Serial.Recent()
				if len(data) == 0 {
					return "(暂无数据)", nil
				}
				if len(data) > 8192 {
					data = data[len(data)-8192:]
				}
				return strings.ToValidUTF8(string(data), "�"), nil
			},
		},
		{
			name:        "serial_write",
			description: "向串口发送数据（会自动追加换行）",
			mutating:    true,
			schema:      obj(map[string]any{"data": sType()}, "data"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				if m.deps.Serial == nil || !m.deps.Serial.IsOpen() {
					return "", fmt.Errorf("串口未打开")
				}
				data := argString(args, "data")
				if !strings.HasSuffix(data, "\n") {
					data += "\n"
				}
				if err := m.deps.Serial.Write([]byte(data)); err != nil {
					return "", err
				}
				return "已发送: " + strconv.Quote(strings.TrimRight(data, "\n")), nil
			},
		},
		{
			name:        "serial_exec",
			description: "通过串口向设备发送命令并抓取回显（需要设备侧有 shell，串口已打开）",
			mutating:    true,
			schema:      obj(map[string]any{"command": sType()}, "command"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				if m.deps.Serial == nil || !m.deps.Serial.IsOpen() {
					return "", fmt.Errorf("串口未打开")
				}
				out, err := m.deps.Serial.RunCapture(argString(args, "command"), 500*time.Millisecond, 8*time.Second)
				if err != nil {
					return "", err
				}
				return truncate(strings.TrimSpace(out), 16*1024), nil
			},
		},
		{
			name:        "ssh_status",
			description: "查询 SSH 会话是否已连接及其目标",
			schema:      obj(nil),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				if m.deps.SSH == nil || !m.deps.SSH.IsConnected() {
					return "SSH 未连接", nil
				}
				return "SSH 已连接: " + m.deps.SSH.Target(), nil
			},
		},
		{
			name:        "ssh_exec",
			description: "在 Remote（远端设备）上通过 SSH 执行一条 shell 命令并返回输出",
			mutating:    true,
			schema:      obj(map[string]any{"command": sType()}, "command"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				if m.deps.SSH == nil || !m.deps.SSH.IsConnected() {
					return "", fmt.Errorf("SSH 未连接")
				}
				return m.deps.SSH.ExecCapture(argString(args, "command"), 64*1024)
			},
		},
		{
			name:        "sftp_status",
			description: "查询 SFTP 文件传输会话是否已连接",
			schema:      obj(nil),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				if m.deps.SFTP == nil || !m.deps.SFTP.IsConnected() {
					return "SFTP 未连接", nil
				}
				return "SFTP 已连接", nil
			},
		},
		{
			name:        "sftp_list",
			description: "列出 SFTP 远端目录内容",
			schema:      obj(map[string]any{"path": sType()}, "path"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				if m.deps.SFTP == nil || !m.deps.SFTP.IsConnected() {
					return "", fmt.Errorf("SFTP 未连接")
				}
				entries, err := m.deps.SFTP.List(argString(args, "path"))
				if err != nil {
					return "", err
				}
				var b strings.Builder
				for _, e := range entries {
					kind := "file"
					if e.IsDir {
						kind = "dir "
					}
					fmt.Fprintf(&b, "%s %10d  %s\n", kind, e.Size, e.Name)
				}
				return b.String(), nil
			},
		},
		{
			name:        "sftp_download",
			description: "把远端文件下载到本地工作区，返回本地路径",
			schema:      obj(map[string]any{"path": sType()}, "path"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				if m.deps.SFTP == nil || !m.deps.SFTP.IsConnected() {
					return "", fmt.Errorf("SFTP 未就绪（请先连接 SSH）")
				}
				remote := argString(args, "path")
				data, err := m.deps.SFTP.Download(remote)
				if err != nil {
					return "", err
				}
				local, err := workspace.Write(filepath.Base(remote), data)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("已下载 %s → %s（%d 字节）", remote, local, len(data)), nil
			},
		},
		{
			name:        "workspace_list",
			description: "列出本地工作区目录内容",
			schema:      obj(map[string]any{"path": sType()}),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				list, err := workspace.List(argString(args, "path"))
				if err != nil {
					return "", err
				}
				if len(list) == 0 {
					return "(空目录)", nil
				}
				var b strings.Builder
				for _, e := range list {
					kind := "file"
					if e.IsDir {
						kind = "dir "
					}
					fmt.Fprintf(&b, "%s %10d  %s\n", kind, e.Size, e.Name)
				}
				return b.String(), nil
			},
		},
		{
			name:        "workspace_read",
			description: "读取本地工作区中的文本文件",
			schema:      obj(map[string]any{"path": sType()}, "path"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				data, err := workspace.Read(argString(args, "path"))
				if err != nil {
					return "", err
				}
				return truncate(strings.ToValidUTF8(string(data), "\uFFFD"), 16*1024), nil
			},
		},
		{
			name:        "workspace_write",
			description: "在本地工作区写入文本文件（固件、配置、脚本等）",
			mutating:    true,
			schema:      obj(map[string]any{"path": sType(), "content": sType()}, "path", "content"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				local, err := workspace.Write(argString(args, "path"), []byte(argString(args, "content")))
				if err != nil {
					return "", err
				}
				return "已写入 " + local, nil
			},
		},
		{
			name:        "sftp_upload",
			description: "把文本内容写入 SFTP 远端文件",
			mutating:    true,
			schema:      obj(map[string]any{"path": sType(), "content": sType()}, "path", "content"),
			run: func(ctx context.Context, args map[string]any) (string, error) {
				if m.deps.SFTP == nil || !m.deps.SFTP.IsConnected() {
					return "", fmt.Errorf("SFTP 未连接")
				}
				path := argString(args, "path")
				if err := m.deps.SFTP.Upload(path, []byte(argString(args, "content"))); err != nil {
					return "", err
				}
				return "已写入 " + path, nil
			},
		},
	}
	for _, t := range m.tools {
		m.byName[t.name] = t
	}
}
