// Package sshclient wraps golang.org/x/crypto/ssh with the small surface the
// debug console needs: connect, run a one-shot command, and drive an
// interactive PTY shell.
package sshclient

import (
	"fmt"
	"io"
	"sync"
	"time"

	"edgekit/internal/sshutil"

	"golang.org/x/crypto/ssh"
)

// Event kinds reported to the UI.
const (
	KindInfo   = "info"
	KindStdout = "stdout"
	KindStderr = "stderr"
	KindError  = "error"
	KindClosed = "closed"
)

// Config holds the parameters required to establish a connection.
type Config = sshutil.Config

// Event is a chunk of remote output or a lifecycle notification.
type Event struct {
	Kind string    `json:"kind"`
	Data []byte    `json:"data"`
	Time time.Time `json:"time"`
}

// Manager owns one SSH connection and at most one active session.
type Manager struct {
	mu        sync.Mutex
	client    *ssh.Client
	cfg       Config
	connected bool

	session *ssh.Session
	stdin   io.WriteCloser
	shell   bool

	// opMu serializes session lifecycle (Run / StartShell) so the state lock
	// never has to be held across blocking network I/O.
	opMu sync.Mutex
	// writeMu serializes writes to the shell stdin without holding mu.
	writeMu sync.Mutex

	onEvent func(Event)
}

// New creates a manager that reports activity through onEvent.
func New(onEvent func(Event)) *Manager {
	return &Manager{onEvent: onEvent}
}

// Connect dials the remote host. It accepts either a password, a private key,
// or both. The host key is not verified (debug tool); its fingerprint is
// reported instead.
func (m *Manager) Connect(cfg Config) error {
	if cfg.Port == 0 {
		cfg.Port = 22
	}

	m.mu.Lock()
	if m.connected {
		host := m.cfg.Host
		m.mu.Unlock()
		return fmt.Errorf("已经连接到 %s", host)
	}
	m.mu.Unlock()

	client, err := sshutil.Dial(cfg, func(keyType, fingerprint string) {
		m.emit(Event{
			Kind: KindInfo,
			Data: []byte(fmt.Sprintf("主机密钥 %s %s", keyType, fingerprint)),
			Time: time.Now(),
		})
	})
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.client = client
	m.cfg = cfg
	m.connected = true
	m.mu.Unlock()

	m.emit(Event{
		Kind: KindInfo,
		Data: []byte(fmt.Sprintf("已连接 %s@%s:%d", cfg.User, cfg.Host, cfg.Port)),
		Time: time.Now(),
	})
	return nil
}

// Disconnect closes any active session and the underlying connection.
func (m *Manager) Disconnect() error {
	m.mu.Lock()
	session := m.session
	stdin := m.stdin
	client := m.client
	wasConnected := m.connected
	m.session = nil
	m.stdin = nil
	m.shell = false
	m.client = nil
	m.connected = false
	m.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if session != nil {
		_ = session.Close()
	}
	if client != nil {
		_ = client.Close()
	}
	if wasConnected {
		m.emit(Event{Kind: KindInfo, Data: []byte("连接已断开"), Time: time.Now()})
	}
	return nil
}

// Run executes a single command and streams its output as events.
func (m *Manager) Run(command string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if !m.connected || m.client == nil {
		m.mu.Unlock()
		return fmt.Errorf("SSH 未连接")
	}
	if m.session != nil {
		m.mu.Unlock()
		return fmt.Errorf("已有会话在运行，请先关闭 Shell")
	}
	client := m.client
	m.mu.Unlock()

	// NewSession is network I/O: do it outside the state lock.
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	session.Stdout = eventWriter{emit: m.emit, kind: KindStdout}
	session.Stderr = eventWriter{emit: m.emit, kind: KindStderr}

	m.mu.Lock()
	m.session = session
	m.mu.Unlock()

	m.emit(Event{Kind: KindInfo, Data: []byte("$ " + command), Time: time.Now()})
	runErr := session.Run(command)

	m.mu.Lock()
	if m.session == session {
		m.session = nil
	}
	m.mu.Unlock()
	_ = session.Close()

	if runErr != nil {
		return fmt.Errorf("命令执行失败: %w", runErr)
	}
	return nil
}

// StartShell requests a PTY and starts an interactive shell.
func (m *Manager) StartShell(cols, rows int) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if !m.connected || m.client == nil {
		m.mu.Unlock()
		return fmt.Errorf("SSH 未连接")
	}
	if m.session != nil {
		m.mu.Unlock()
		return fmt.Errorf("已有会话在运行")
	}
	if cols <= 0 {
		cols = 100
	}
	if rows <= 0 {
		rows = 30
	}
	client := m.client
	m.mu.Unlock()

	// The whole PTY setup is network I/O: do it outside the state lock.
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		_ = session.Close()
		return fmt.Errorf("申请 PTY 失败: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("获取输入管道失败: %w", err)
	}
	session.Stdout = eventWriter{emit: m.emit, kind: KindStdout}
	session.Stderr = eventWriter{emit: m.emit, kind: KindStderr}
	if err := session.Shell(); err != nil {
		_ = session.Close()
		return fmt.Errorf("启动 Shell 失败: %w", err)
	}

	m.mu.Lock()
	m.session = session
	m.stdin = stdin
	m.shell = true
	m.mu.Unlock()

	m.emit(Event{Kind: KindInfo, Data: []byte("交互式 Shell 已启动"), Time: time.Now()})

	go func() {
		waitErr := session.Wait()
		m.mu.Lock()
		if m.session == session {
			m.session = nil
			m.stdin = nil
			m.shell = false
		}
		m.mu.Unlock()
		_ = session.Close()
		if waitErr != nil {
			m.emit(Event{Kind: KindClosed, Data: []byte("Shell 已结束: " + waitErr.Error()), Time: time.Now()})
		} else {
			m.emit(Event{Kind: KindClosed, Data: []byte("Shell 已结束"), Time: time.Now()})
		}
	}()
	return nil
}

// CloseShell terminates the interactive shell without dropping the connection.
func (m *Manager) CloseShell() error {
	m.mu.Lock()
	session := m.session
	stdin := m.stdin
	shell := m.shell
	m.session = nil
	m.stdin = nil
	m.shell = false
	m.mu.Unlock()

	if !shell {
		return nil
	}
	if stdin != nil {
		_ = stdin.Close()
	}
	if session != nil {
		_ = session.Close()
	}
	return nil
}

// WriteShell forwards raw bytes to the interactive shell's stdin.
func (m *Manager) WriteShell(data []byte) error {
	m.mu.Lock()
	stdin := m.stdin
	ok := m.shell && stdin != nil
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("Shell 未打开")
	}
	// Serialize writes without holding the state lock across the network write.
	m.writeMu.Lock()
	_, err := stdin.Write(data)
	m.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("写入 Shell 失败: %w", err)
	}
	return nil
}

// ResizeShell notifies the remote PTY of a new window size.
func (m *Manager) ResizeShell(cols, rows int) error {
	m.mu.Lock()
	session := m.session
	shell := m.shell
	m.mu.Unlock()
	if !shell || session == nil {
		return nil
	}
	return session.WindowChange(rows, cols)
}

// IsConnected reports whether the transport is up.
func (m *Manager) IsConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

// HasShell reports whether an interactive shell session is active.
func (m *Manager) HasShell() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shell
}

// ExecCapture runs a command on a dedicated session and returns its combined
// output (bounded by maxBytes). An interactive shell session is unaffected.
func (m *Manager) ExecCapture(command string, maxBytes int) (string, error) {
	m.mu.Lock()
	if !m.connected || m.client == nil {
		m.mu.Unlock()
		return "", fmt.Errorf("SSH 未连接")
	}
	client := m.client
	m.mu.Unlock()

	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建会话失败: %w", err)
	}
	defer session.Close()

	buf := &limitedBuffer{max: maxBytes}
	session.Stdout = buf
	session.Stderr = buf
	runErr := session.Run(command)
	out := buf.String()
	if runErr != nil {
		if out == "" {
			return "", fmt.Errorf("命令执行失败: %w", runErr)
		}
		out += "\n(exit: " + runErr.Error() + ")"
	}
	return out, nil
}

// RawClient exposes the underlying SSH client so other subsystems (SFTP) can
// share the same connection. It returns nil when not connected.
func (m *Manager) RawClient() *ssh.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.client
}

// Target returns a human-readable description of the connection.
func (m *Manager) Target() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.connected {
		return ""
	}
	return fmt.Sprintf("%s@%s:%d", m.cfg.User, m.cfg.Host, m.cfg.Port)
}

// limitedBuffer collects at most max bytes.
type limitedBuffer struct {
	max       int
	buf       []byte
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
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

func (b *limitedBuffer) String() string {
	s := string(b.buf)
	if b.truncated {
		s += "\n…(输出已截断)"
	}
	return s
}

// Config returns the parameters of the active (or most recent) connection.
func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *Manager) emit(ev Event) {
	if m.onEvent != nil {
		m.onEvent(ev)
	}
}

// eventWriter adapts the emit callback to io.Writer for ssh.Session.
type eventWriter struct {
	emit func(Event)
	kind string
}

func (w eventWriter) Write(p []byte) (int, error) {
	chunk := make([]byte, len(p))
	copy(chunk, p)
	w.emit(Event{Kind: w.kind, Data: chunk, Time: time.Now()})
	return len(p), nil
}
