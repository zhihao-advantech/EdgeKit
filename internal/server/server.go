// Package server hosts the embedded web UI over a loopback HTTP listener and
// bridges it to the serial and SSH backends through a JSON WebSocket protocol.
//
// Serial and SSH connections are kept in a session registry: several devices
// can be connected at once (up to a handful), the UI switches between them by
// session id, and the agent acts on the currently focused session.
package server

import (
	"context"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"edgekit/internal/agent"
	"edgekit/internal/kit"
	"edgekit/internal/kits"
	"edgekit/internal/policy"
	"edgekit/internal/runtime"
	"edgekit/internal/serial"
	"edgekit/internal/sftpx"
	"edgekit/internal/sshclient"
	"edgekit/internal/timeline"
	"edgekit/internal/workspace"

	"github.com/gorilla/websocket"
)

//go:embed all:web
var webAssets embed.FS

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// The listener is bound to loopback and only used by the local webview.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// message is the envelope exchanged with the UI.
type message struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// deviceSession is one connected serial port or SSH host.
type deviceSession struct {
	id     string
	kind   string // "serial" | "ssh"
	label  string
	serial *serial.Manager    // when kind == "serial"
	ssh    *sshclient.Manager // when kind == "ssh"
	sftp   *sftpx.Manager     // when kind == "ssh"
	tl     *timeline.Timeline // per-device record (the single source of observations)
}

// timelineMax bounds the per-device record kept in memory.
const timelineMax = 5000

// Server wires the backends to the browser clients.
type Server struct {
	agent *agent.Manager
	deps  kit.Deps
	kits  *kit.Registry
	gate  *policy.Gate
	batch *streamBatcher

	mu       sync.Mutex
	sessions map[string]*deviceSession
	order    []string
	seq      int
	focus    string

	clients map[*client]struct{}

	ln   net.Listener
	http *http.Server
	url  string
}

// New builds a server and its backends.
func New() *Server {
	s := &Server{
		sessions: make(map[string]*deviceSession),
		clients:  make(map[*client]struct{}),
	}
	// The focused-session proxies are the capabilities every built-in kit and
	// the agent resolve against, so a tool call always acts on the selected device.
	s.deps = kit.Deps{
		Serial: focusedSerial{s},
		SSH:    focusedSSH{s},
		SFTP:   focusedSFTP{s},
	}
	s.kits = kit.NewRegistry()
	for _, k := range kits.Builtin(s.deps) {
		s.kits.Register(k)
	}
	s.gate = policy.New(s.onApprovalRequest)
	s.agent = agent.New(s.kits, s.deps, s.gate, s.onAgentEvent)
	s.batch = newStreamBatcher(s.emitStream)
	// Preload the persisted model config so the agent works even when driven
	// without the UI (e.g. by an external tool over the WebSocket API).
	if st := loadSettings(); len(st) > 0 {
		s.agent.SetConfig(agent.Config{
			BaseURL:     settingString(st, "in-agent-base"),
			APIKey:      settingString(st, "in-agent-key"),
			Model:       settingString(st, "in-agent-model"),
			AutoRun:     settingBool(st, "chk-agent-auto"),
			Target:      settingString(st, "agent-target"),
			Backend:     settingString(st, "agent-backend"),
			ACPCommand:  settingString(st, "agent-acp-command"),
			ACPArgs:     settingStrings(st, "agent-acp-args"),
			ACPOverride: settingBool(st, "agent-acp-override"),
			ACPModel:    settingString(st, "agent-acp-model"),
		})
		s.gate.SetAutoRun(settingBool(st, "chk-agent-auto"))
		for _, id := range settingStrings(st, "kits.disabled") {
			s.kits.SetEnabled(id, false)
		}
	}
	return s
}

func settingString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func settingStrings(m map[string]any, key string) []string {
	var out []string
	switch v := m[key].(type) {
	case []string:
		out = v
	case []any:
		for _, it := range v {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func settingBool(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

/* ------------------------------------------------------------------ *
 * session registry
 * ------------------------------------------------------------------ */

// session returns the session with the given id, or the focused one when id is
// empty. It returns nil when there is none.
func (s *Server) session(id string) *deviceSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		id = s.focus
	}
	return s.sessions[id]
}

func (s *Server) nextID(kind string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("%s-%d", kind, s.seq)
}

// registerSession stores ds and notifies clients.
func (s *Server) registerSession(ds *deviceSession) {
	s.mu.Lock()
	s.sessions[ds.id] = ds
	s.order = append(s.order, ds.id)
	if s.focus == "" {
		s.focus = ds.id
	}
	focus := s.focus
	s.mu.Unlock()

	s.broadcast("session.opened", sessionInfo(ds))
	if focus == ds.id {
		s.broadcast("session.focus", map[string]any{"id": focus})
	}
}

// closeSession tears down a session and notifies clients.
func (s *Server) closeSession(id string) {
	s.mu.Lock()
	ds := s.sessions[id]
	delete(s.sessions, id)
	for i, v := range s.order {
		if v == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	if s.focus == id {
		s.focus = ""
		if len(s.order) > 0 {
			s.focus = s.order[len(s.order)-1]
		}
	}
	focus := s.focus
	s.mu.Unlock()

	if ds != nil {
		if ds.serial != nil {
			_ = ds.serial.Close()
		}
		if ds.ssh != nil {
			_ = ds.ssh.Disconnect()
		}
		if ds.sftp != nil {
			ds.sftp.Detach()
		}
	}
	s.broadcast("session.closed", map[string]any{"id": id, "focus": focus})
	if focus != "" {
		s.broadcast("session.focus", map[string]any{"id": focus})
	}
}

func sessionInfo(ds *deviceSession) map[string]any {
	item := map[string]any{"id": ds.id, "kind": ds.kind, "label": ds.label}
	switch ds.kind {
	case "serial":
		item["connected"] = ds.serial.IsOpen()
		item["config"] = ds.serial.Config()
	case "ssh":
		item["connected"] = ds.ssh.IsConnected()
		item["shell"] = ds.ssh.HasShell()
		item["sftp"] = ds.sftp.IsConnected()
		item["config"] = ds.ssh.Config()
	}
	return item
}

func (s *Server) sessionsSnapshot() map[string]any {
	s.mu.Lock()
	order := append([]string(nil), s.order...)
	focus := s.focus
	s.mu.Unlock()

	list := make([]map[string]any, 0, len(order))
	for _, id := range order {
		if ds := s.session(id); ds != nil {
			list = append(list, sessionInfo(ds))
		}
	}
	return map[string]any{"sessions": list, "focus": focus}
}

func (s *Server) setFocus(id string) {
	s.mu.Lock()
	if _, ok := s.sessions[id]; ok {
		s.focus = id
	}
	focus := s.focus
	s.mu.Unlock()
	s.broadcast("session.focus", map[string]any{"id": focus})
}

/* ------------------------------------------------------------------ *
 * agent accessors (resolve the focused session on every call)
 * ------------------------------------------------------------------ */

type focusedSerial struct{ s *Server }

func (f focusedSerial) manager() *serial.Manager {
	if ds := f.s.session(""); ds != nil && ds.serial != nil {
		return ds.serial
	}
	return nil
}
func (f focusedSerial) IsOpen() bool { m := f.manager(); return m != nil && m.IsOpen() }
func (f focusedSerial) Port() string {
	if m := f.manager(); m != nil {
		return m.Port()
	}
	return ""
}
func (f focusedSerial) Write(p []byte) error {
	if m := f.manager(); m != nil {
		return m.Write(p)
	}
	return fmt.Errorf("串口未打开")
}
func (f focusedSerial) Recent() []byte {
	if m := f.manager(); m != nil {
		return m.Recent()
	}
	return nil
}
func (f focusedSerial) RunCapture(cmd string, quiet, timeout time.Duration) (string, error) {
	if m := f.manager(); m != nil {
		return m.RunCapture(cmd, quiet, timeout)
	}
	return "", fmt.Errorf("串口未打开")
}

type focusedSSH struct{ s *Server }

func (f focusedSSH) manager() *sshclient.Manager {
	if ds := f.s.session(""); ds != nil && ds.ssh != nil {
		return ds.ssh
	}
	return nil
}
func (f focusedSSH) IsConnected() bool { m := f.manager(); return m != nil && m.IsConnected() }
func (f focusedSSH) Target() string {
	if m := f.manager(); m != nil {
		return m.Target()
	}
	return ""
}
func (f focusedSSH) ExecCapture(cmd string, maxBytes int) (string, error) {
	if m := f.manager(); m != nil {
		return m.ExecCapture(cmd, maxBytes)
	}
	return "", fmt.Errorf("SSH 未连接")
}

type focusedSFTP struct{ s *Server }

func (f focusedSFTP) manager() *sftpx.Manager {
	if ds := f.s.session(""); ds != nil && ds.sftp != nil {
		return ds.sftp
	}
	return nil
}
func (f focusedSFTP) IsConnected() bool { m := f.manager(); return m != nil && m.IsConnected() }
func (f focusedSFTP) List(p string) ([]sftpx.Entry, error) {
	if m := f.manager(); m != nil {
		return m.List(p)
	}
	return nil, fmt.Errorf("SFTP 未就绪")
}
func (f focusedSFTP) Download(p string) ([]byte, error) {
	if m := f.manager(); m != nil {
		return m.Download(p)
	}
	return nil, fmt.Errorf("SFTP 未就绪")
}
func (f focusedSFTP) Upload(p string, data []byte) error {
	if m := f.manager(); m != nil {
		return m.Upload(p, data)
	}
	return fmt.Errorf("SFTP 未就绪")
}

/* ------------------------------------------------------------------ *
 * http / websocket plumbing
 * ------------------------------------------------------------------ */

// Start listens on addr (loopback by default) and returns the WebSocket URL the
// embedded UI should connect to. Only the /ws endpoint is exposed; the UI
// itself is loaded directly into the WebView, not served over HTTP.
func (s *Server) Start(addr string) (string, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}
	s.ln = ln
	s.url = "ws://" + ln.Addr().String() + "/ws"
	if err := runtime.Write(runtime.Endpoint{WS: s.url, PID: os.Getpid(), Started: time.Now()}); err != nil {
		log.Printf("发布运行端点失败: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)

	s.http = &http.Server{Handler: mux}
	go func() {
		if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("http server stopped: %v", err)
		}
	}()
	return s.url, nil
}

// URL returns the WebSocket endpoint the embedded UI connects to.
func (s *Server) URL() string { return s.url }

// Page returns the single-file UI (HTML with CSS and JS inlined) ready to be
// loaded straight into the WebView via SetHtml. wsURL is injected so the page
// knows where to open its WebSocket connection.
func Page(wsURL string) (string, error) {
	html, err := webAssets.ReadFile("web/index.html")
	if err != nil {
		return "", fmt.Errorf("read index.html: %w", err)
	}
	css, err := webAssets.ReadFile("web/css/style.css")
	if err != nil {
		return "", fmt.Errorf("read style.css: %w", err)
	}
	js, err := webAssets.ReadFile("web/js/app.js")
	if err != nil {
		return "", fmt.Errorf("read app.js: %w", err)
	}

	page := strings.Replace(string(html), "{{WS_URL}}", wsURL, 1)
	page = strings.Replace(page, `<link rel="stylesheet" href="css/style.css">`, "<style>\n"+string(css)+"\n</style>", 1)
	page = strings.Replace(page, `<script src="js/app.js"></script>`, "<script>\n"+string(js)+"\n</script>", 1)
	return page, nil
}

// Close tears down the backends, clients and the HTTP listener.
func (s *Server) Close() error {
	s.mu.Lock()
	ids := append([]string(nil), s.order...)
	s.mu.Unlock()
	for _, id := range ids {
		s.closeSession(id)
	}
	s.agent.Cancel()
	s.agent.Close()
	runtime.Clear()
	s.batch.flushAll()

	s.mu.Lock()
	clients := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.clients = make(map[*client]struct{})
	s.mu.Unlock()
	for _, c := range clients {
		c.close()
	}

	if s.http != nil {
		return s.http.Close()
	}
	return nil
}

// client is a single WebSocket peer.
type client struct {
	conn *websocket.Conn
	send chan []byte
	srv  *Server

	// inbox decouples reading from handling: the reader only enqueues, a single
	// worker dispatches in order, so a slow handler never blocks the socket.
	inbox chan message
	quit  chan struct{}

	mu     sync.Mutex
	closed bool
}

// enqueue hands a message to the client's send buffer without blocking. It is a
// no-op once the client is closed, so it can never send on a closed channel.
func (c *client) enqueue(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.send <- b:
	default:
		// Slow client: drop rather than block the producer.
	}
}

// close shuts the client down exactly once: the send buffer is closed (so
// writePump exits) and the worker is told to stop.
func (c *client) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.send)
	close(c.quit)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	c := &client{
		conn:  conn,
		send:  make(chan []byte, 512),
		srv:   s,
		inbox: make(chan message, 1024),
		quit:  make(chan struct{}),
	}

	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()

	log.Printf("界面已连接 (%s)", r.RemoteAddr)
	go c.writePump()
	go c.worker()
	s.sendStatus(c)
	c.readPump()
	log.Printf("界面已断开 (%s)", r.RemoteAddr)
}

// worker dispatches this client's messages in arrival order, off the read loop.
func (c *client) worker() {
	for {
		select {
		case <-c.quit:
			return
		case msg := <-c.inbox:
			c.srv.dispatch(c, msg)
		}
	}
}

func (c *client) readPump() {
	defer func() {
		c.srv.removeClient(c)
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(4 << 20)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var msg message
		if err := json.Unmarshal(data, &msg); err != nil {
			c.srv.sendError(c, fmt.Errorf("无效消息: %w", err))
			continue
		}
		select {
		case c.inbox <- msg:
		case <-c.quit:
			return
		}
	}
}

func (c *client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (s *Server) removeClient(c *client) {
	s.mu.Lock()
	_, ok := s.clients[c]
	delete(s.clients, c)
	s.mu.Unlock()
	if ok {
		c.close()
	}
}

// envelope is the outbound message shape.
type envelope struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId,omitempty"`
	Payload   any    `json:"payload,omitempty"`
}

func (s *Server) sendTo(c *client, typ string, payload any) {
	b, err := json.Marshal(envelope{Type: typ, Payload: payload})
	if err != nil {
		return
	}
	c.enqueue(b)
}

func (s *Server) broadcast(typ string, payload any) {
	b, err := json.Marshal(envelope{Type: typ, Payload: payload})
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		c.enqueue(b)
	}
}

func (s *Server) sendError(c *client, err error) {
	s.sendTo(c, "error", map[string]any{"message": err.Error()})
}

/* ------------------------------------------------------------------ *
 * backend events
 * ------------------------------------------------------------------ */

func (s *Server) onSerialEvent(id string, ev serial.Event) {
	s.record(id, timeline.Record{Channel: timeline.ChannelSerial, Kind: ev.Direction, Time: ev.Time, Data: ev.Data})
	batchable := ev.Direction == serial.DirRX || ev.Direction == serial.DirTX
	s.batch.add("serial", id, ev.Direction, ev.Data, ev.Time, batchable)
}

func (s *Server) onSSHEvent(id string, ev sshclient.Event) {
	s.record(id, timeline.Record{Channel: timeline.ChannelSSH, Kind: ev.Kind, Time: ev.Time, Data: ev.Data})
	batchable := ev.Kind == sshclient.KindStdout || ev.Kind == sshclient.KindStderr
	s.batch.add("ssh", id, ev.Kind, ev.Data, ev.Time, batchable)
	if ev.Kind == sshclient.KindClosed {
		// The transport is gone, so the SFTP subsystem is gone with it.
		if ds := s.session(id); ds != nil {
			if ds.sftp != nil {
				ds.sftp.Detach()
			}
			s.broadcastSSHStatus(ds)
		}
	}
}

// emitStream broadcasts a (possibly coalesced) data chunk.
func (s *Server) emitStream(channel, id, kind string, data []byte, ts time.Time) {
	switch channel {
	case "serial":
		s.broadcast("serial.event", map[string]any{
			"sessionId": id, "direction": kind, "data": data, "time": ts,
		})
	case "ssh":
		s.broadcast("ssh.event", map[string]any{
			"sessionId": id, "kind": kind, "data": data, "time": ts,
		})
	}
}

func (s *Server) onAgentEvent(ev agent.Event) {
	s.broadcast("agent.event", ev)
}

// onApprovalRequest turns a policy prompt into an agent approval event, which
// the UI already renders as an approval card.
func (s *Server) onApprovalRequest(req policy.Request) {
	s.broadcast("agent.event", agent.Event{
		Kind:  agent.KindApproval,
		ID:    req.ID,
		Tool:  req.Tool,
		Args:  req.Args,
		State: "pending",
		Time:  time.Now(),
	})
}

// handleToolsList returns the registry's tool definitions (used by the MCP bridge).
func (s *Server) handleToolsList(c *client) {
	tools := s.kits.Tools()
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"risk":        string(t.Risk),
			"schema":      t.Schema,
		})
	}
	s.sendTo(c, "tools.defs", map[string]any{"tools": out})
}

// handleToolCall runs a registry tool on behalf of an external caller (the MCP
// bridge), after the policy gate has had its say.
func (s *Server) handleToolCall(c *client, msg message) {
	var p struct {
		ID   string         `json:"id"`
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	t, ok := s.kits.Tool(p.Name)
	if !ok {
		s.sendTo(c, "tool.result", map[string]any{"id": p.ID, "name": p.Name, "ok": false, "error": "未知工具: " + p.Name})
		return
	}
	argBytes, _ := json.Marshal(p.Args)
	argText := string(argBytes)

	// Audit: record the request on the focused device's timeline (best effort).
	s.record("", timeline.Record{Channel: timeline.ChannelAgent, Kind: "action", Data: []byte(p.Name + " " + argText)})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.gate.Check(ctx, p.Name, t.Risk, argText); err != nil {
		s.sendTo(c, "tool.result", map[string]any{"id": p.ID, "name": p.Name, "ok": false, "error": err.Error()})
		return
	}
	out, err := t.Call(ctx, p.Args)
	if err != nil {
		s.sendTo(c, "tool.result", map[string]any{"id": p.ID, "name": p.Name, "ok": false, "error": err.Error()})
		return
	}
	s.sendTo(c, "tool.result", map[string]any{"id": p.ID, "name": p.Name, "ok": true, "output": out})
}

// record appends an observation to the device's timeline.
func (s *Server) record(id string, r timeline.Record) {
	if ds := s.session(id); ds != nil && ds.tl != nil {
		ds.tl.Append(r)
	}
}

// handleTimeline replays a device's record to a client (used for UI replay and
// by the agent for cross-channel correlation).
func (s *Server) handleTimeline(c *client, msg message) {
	var p struct {
		Since uint64 `json:"since"`
		Limit int    `json:"limit"`
	}
	_ = json.Unmarshal(msg.Payload, &p)
	ds := s.session(msg.SessionID)
	if ds == nil || ds.tl == nil {
		s.sendError(c, fmt.Errorf("会话不存在"))
		return
	}
	s.sendTo(c, "timeline.records", map[string]any{
		"sessionId": ds.id,
		"records":   ds.tl.Since(p.Since, p.Limit),
	})
}

func (s *Server) broadcastSerialStatus(ds *deviceSession) {
	s.broadcast("serial.status", map[string]any{
		"sessionId": ds.id,
		"open":      ds.serial.IsOpen(),
		"config":    ds.serial.Config(),
	})
}

func (s *Server) broadcastSSHStatus(ds *deviceSession) {
	s.broadcast("ssh.status", map[string]any{
		"sessionId": ds.id,
		"connected": ds.ssh.IsConnected(),
		"shell":     ds.ssh.HasShell(),
		"sftp":      ds.sftp.IsConnected(),
		"config":    ds.ssh.Config(),
	})
}

func (s *Server) sendStatus(c *client) {
	s.sendTo(c, "sessions", s.sessionsSnapshot())
	s.sendTo(c, "agent.config", s.agent.Config())
	s.sendTo(c, "settings", loadSettings())
	s.sendTo(c, "kits", s.kitsPayload())
}

// kitView is one kit as the UI sees it.
type kitView struct {
	kit.Manifest
	Enabled bool       `json:"enabled"`
	Tools   []toolView `json:"tools"`
}

// toolView is one contributed tool as the UI sees it.
type toolView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
}

// kitsPayload describes the installed kits, their activation state and the
// tools each one contributes.
func (s *Server) kitsPayload() map[string]any {
	manifests := s.kits.Manifests()
	views := make([]kitView, 0, len(manifests))
	for _, m := range manifests {
		tools := s.kits.KitTools(m.ID)
		tv := make([]toolView, 0, len(tools))
		for _, t := range tools {
			tv = append(tv, toolView{Name: t.Name, Description: t.Description, Risk: string(t.Risk)})
		}
		views = append(views, kitView{Manifest: m, Enabled: s.kits.IsEnabled(m.ID), Tools: tv})
	}
	return map[string]any{"kits": views}
}

// handleKitsSetEnabled activates or deactivates a kit and persists the choice.
func (s *Server) handleKitsSetEnabled(c *client, msg message) {
	var p struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if !s.kits.SetEnabled(p.ID, p.Enabled) {
		s.sendError(c, fmt.Errorf("未知 Kit: %s", p.ID))
		return
	}
	s.persistKits()
	s.broadcast("kits", s.kitsPayload())
}

// persistKits remembers which kits are disabled.
func (s *Server) persistKits() {
	st := loadSettings()
	disabled := []string{}
	for _, m := range s.kits.Manifests() {
		if !s.kits.IsEnabled(m.ID) {
			disabled = append(disabled, m.ID)
		}
	}
	st["kits.disabled"] = disabled
	_ = saveSettings(st)
}

/* ------------------------------------------------------------------ *
 * settings file
 * ------------------------------------------------------------------ */

// settingsPath returns the per-user settings file.
func settingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "edgekit")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "settings.json")
}

// loadSettings reads the persisted UI settings (empty map when absent).
func loadSettings() map[string]any {
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		return map[string]any{}
	}
	m := map[string]any{}
	if json.Unmarshal(data, &m) != nil {
		return map[string]any{}
	}
	return m
}

// saveSettings writes the UI settings with user-only permissions.
func saveSettings(m map[string]any) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath(), data, 0o600)
}

/* ------------------------------------------------------------------ *
 * dispatch
 * ------------------------------------------------------------------ */

func (s *Server) dispatch(c *client, msg message) {
	switch msg.Type {
	case "serial.list":
		s.handleSerialList(c)
	case "serial.open":
		s.handleSerialOpen(c, msg.Payload)
	case "serial.close":
		s.closeSession(msg.SessionID)
	case "serial.write":
		s.handleSerialWrite(c, msg)
	case "ssh.connect":
		s.handleSSHConnect(c, msg.Payload)
	case "ssh.disconnect":
		s.closeSession(msg.SessionID)
	case "ssh.exec":
		s.handleSSHExec(c, msg)
	case "ssh.shell.start":
		s.handleShellStart(c, msg)
	case "ssh.shell.write":
		s.handleShellWrite(c, msg)
	case "ssh.shell.resize":
		s.handleShellResize(c, msg)
	case "ssh.shell.close":
		if ds := s.session(msg.SessionID); ds != nil && ds.ssh != nil {
			if err := ds.ssh.CloseShell(); err != nil {
				s.sendError(c, err)
			}
			s.broadcastSSHStatus(ds)
		}
	case "session.focus":
		s.setFocus(msg.SessionID)
	case "timeline":
		s.handleTimeline(c, msg)
	case "kits.setEnabled":
		s.handleKitsSetEnabled(c, msg)
	case "tools.list":
		s.handleToolsList(c)
	case "tool.call":
		s.handleToolCall(c, msg)
	case "session.close":
		s.closeSession(msg.SessionID)
	case "agent.send":
		s.handleAgentSend(msg.Payload)
	case "agent.config":
		s.handleAgentConfig(msg.Payload)
	case "agent.cancel":
		s.agent.Cancel()
	case "agent.reset":
		s.agent.Reset()
	case "agent.approve":
		s.handleAgentApprove(msg.Payload)
	case "agent.models":
		s.handleAgentModels()
	case "agent.setModel":
		s.handleAgentSetModel(msg.Payload)
	case "agent.session.new":
		s.handleAgentSessionNew()
	case "agent.session.list":
		s.emitAgentSessions()
	case "agent.session.load":
		s.handleAgentSessionLoad(msg.Payload)
	case "settings.set":
		var p map[string]any
		if err := json.Unmarshal(msg.Payload, &p); err == nil {
			if err := saveSettings(p); err != nil {
				s.sendError(c, err)
			}
		}
	case "fs.list":
		s.handleFSList(c, msg)
	case "fs.mkdir":
		s.handleFSMkdir(c, msg)
	case "fs.newfile":
		s.handleFSNewFile(c, msg)
	case "fs.upload":
		s.handleFSUpload(c, msg)
	case "fs.download":
		s.handleFSDownload(c, msg)
	case "fs.delete":
		s.handleFSDelete(c, msg)
	case "fs.read":
		s.handleFSRead(c, msg)
	case "fs.write":
		s.handleFSWrite(c, msg)
	default:
		s.sendError(c, fmt.Errorf("未知指令: %s", msg.Type))
	}
}

/* ------------------------------------------------------------------ *
 * serial / ssh handlers
 * ------------------------------------------------------------------ */

func (s *Server) handleSerialList(c *client) {
	ports, err := serial.New(nil).List()
	if err != nil {
		s.sendError(c, err)
		return
	}
	s.sendTo(c, "serial.ports", map[string]any{"ports": ports})
}

func (s *Server) handleSerialOpen(c *client, raw json.RawMessage) {
	var cfg serial.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	id := s.nextID("serial")
	ds := &deviceSession{id: id, kind: "serial", label: path.Base(cfg.Port), tl: timeline.New(timelineMax)}
	ds.serial = serial.New(func(ev serial.Event) { s.onSerialEvent(id, ev) })
	if err := ds.serial.Open(cfg); err != nil {
		s.sendError(c, err)
		return
	}
	s.registerSession(ds)
	s.broadcastSerialStatus(ds)
	// Emitted after registration so clients already know the session id.
	s.onSerialEvent(ds.id, serial.Event{
		Direction: serial.DirInfo,
		Data:      []byte(fmt.Sprintf("已打开 %s @ %d %d%s%d", cfg.Port, cfg.Baud, cfg.DataBits, serial.ParityLabel(cfg.Parity), int(cfg.StopBits))),
		Time:      time.Now(),
	})
}

func (s *Server) handleSerialWrite(c *client, msg message) {
	ds := s.session(msg.SessionID)
	if ds == nil || ds.serial == nil {
		s.sendError(c, fmt.Errorf("串口会话不存在"))
		return
	}
	var p struct {
		Data string `json:"data"`
		Hex  bool   `json:"hex"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	var (
		buf []byte
		err error
	)
	if p.Hex {
		buf, err = decodeHex(p.Data)
		if err != nil {
			s.sendError(c, err)
			return
		}
	} else {
		buf = []byte(p.Data)
	}
	if err := ds.serial.Write(buf); err != nil {
		s.sendError(c, err)
	}
}

func (s *Server) handleSSHConnect(c *client, raw json.RawMessage) {
	var cfg sshclient.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	id := s.nextID("ssh")
	label := fmt.Sprintf("%s@%s:%d", cfg.User, cfg.Host, cfg.Port)
	ds := &deviceSession{id: id, kind: "ssh", label: label, tl: timeline.New(timelineMax)}
	ds.ssh = sshclient.New(func(ev sshclient.Event) { s.onSSHEvent(id, ev) })
	ds.sftp = sftpx.New(nil)

	if err := ds.ssh.Connect(cfg); err != nil {
		s.sendError(c, err)
		return
	}
	// Share the same connection for the workspace file panel (SFTP).
	if rc := ds.ssh.RawClient(); rc != nil {
		if err := ds.sftp.Attach(rc, ds.ssh.Target()); err != nil {
			s.sendError(c, err)
		}
	}
	s.registerSession(ds)
	s.broadcastSSHStatus(ds)
	s.onSSHEvent(ds.id, sshclient.Event{
		Kind: sshclient.KindInfo,
		Data: []byte(fmt.Sprintf("已连接 %s@%s:%d", cfg.User, cfg.Host, cfg.Port)),
		Time: time.Now(),
	})
}

func (s *Server) handleSSHExec(c *client, msg message) {
	ds := s.session(msg.SessionID)
	if ds == nil || ds.ssh == nil {
		s.sendError(c, fmt.Errorf("SSH 会话不存在"))
		return
	}
	var p struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if err := ds.ssh.Run(p.Command); err != nil {
		s.sendError(c, err)
	}
}

func (s *Server) handleShellStart(c *client, msg message) {
	ds := s.session(msg.SessionID)
	if ds == nil || ds.ssh == nil {
		s.sendError(c, fmt.Errorf("SSH 会话不存在"))
		return
	}
	var p struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	_ = json.Unmarshal(msg.Payload, &p)
	if err := ds.ssh.StartShell(p.Cols, p.Rows); err != nil {
		s.sendError(c, err)
		return
	}
	s.broadcastSSHStatus(ds)
}

func (s *Server) handleShellWrite(c *client, msg message) {
	ds := s.session(msg.SessionID)
	if ds == nil || ds.ssh == nil {
		s.sendError(c, fmt.Errorf("SSH 会话不存在"))
		return
	}
	var p struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if err := ds.ssh.WriteShell([]byte(p.Data)); err != nil {
		s.sendError(c, err)
	}
}

func (s *Server) handleShellResize(c *client, msg message) {
	ds := s.session(msg.SessionID)
	if ds == nil || ds.ssh == nil {
		return
	}
	var p struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return
	}
	if err := ds.ssh.ResizeShell(p.Cols, p.Rows); err != nil {
		s.sendError(c, err)
	}
}

func (s *Server) handleAgentSend(raw json.RawMessage) {
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	s.agent.Send(p.Text)
}

func (s *Server) handleAgentConfig(raw json.RawMessage) {
	var cfg agent.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return
	}
	s.agent.SetConfig(cfg)
	s.gate.SetAutoRun(cfg.AutoRun)
	s.broadcast("agent.config", s.agent.Config())
}

func (s *Server) handleAgentApprove(raw json.RawMessage) {
	var p struct {
		ID    string `json:"id"`
		Allow bool   `json:"allow"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	s.gate.Approve(p.ID, p.Allow)
}

// handleAgentModels starts the active backend if needed (the ACP agent process
// must run before its model list is known) and broadcasts the selectable models.
func (s *Server) handleAgentModels() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := s.agent.Prepare(ctx); err != nil {
		s.broadcast("agent.models", map[string]any{
			"models": []agent.Model{}, "current": "", "error": err.Error(),
		})
		return
	}
	s.broadcast("agent.models", map[string]any{
		"models": s.agent.Models(), "current": s.agent.CurrentModel(),
	})
}

// handleAgentSetModel switches the model of the active backend.
func (s *Server) handleAgentSetModel(raw json.RawMessage) {
	var p struct {
		ModelID string `json:"modelId"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	msg := map[string]any{"models": s.agent.Models(), "current": s.agent.CurrentModel()}
	if err := s.agent.SetModel(ctx, p.ModelID); err != nil {
		msg["error"] = err.Error()
	} else {
		msg["current"] = s.agent.CurrentModel()
	}
	s.broadcast("agent.models", msg)
}

// handleAgentSessionNew opens a fresh conversation without restarting the agent.
func (s *Server) handleAgentSessionNew() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := s.agent.NewSession(ctx); err != nil {
		s.broadcastAgentSessions(nil, err)
		return
	}
	s.emitAgentSessions()
}

// handleAgentSessionLoad resumes a persisted conversation.
func (s *Server) handleAgentSessionLoad(raw json.RawMessage) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.SessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := s.agent.LoadSession(ctx, p.SessionID); err != nil {
		s.broadcastAgentSessions(nil, err)
		return
	}
	s.emitAgentSessions()
}

// emitAgentSessions lists and broadcasts the agent's persisted sessions.
func (s *Server) emitAgentSessions() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sessions, err := s.agent.ListSessions(ctx)
	s.broadcastAgentSessions(sessions, err)
}

// broadcastAgentSessions sends the session list and current id to every client.
// Backends without session support yield an empty list; err is surfaced inline.
func (s *Server) broadcastAgentSessions(sessions []agent.SessionInfo, err error) {
	if sessions == nil {
		sessions = []agent.SessionInfo{}
	}
	payload := map[string]any{"sessions": sessions, "current": s.agent.CurrentSession()}
	if err != nil {
		payload["error"] = err.Error()
	}
	s.broadcast("agent.sessions", payload)
}

/* ------------------------------------------------------------------ *
 * workspace file operations
 * ------------------------------------------------------------------ */

// fsEntry is the uniform shape sent to the workspace panel.
type fsEntry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"isDir"`
	Mode  string `json:"mode,omitempty"`
	Time  string `json:"time,omitempty"`
}

func (s *Server) handleFSList(c *client, msg message) {
	var p struct {
		Side string `json:"side"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	entries, display, err := s.fsList(p.Side, msg.SessionID, p.Path)
	if err != nil {
		s.sendError(c, err)
		return
	}
	s.sendTo(c, "fs.files", map[string]any{
		"side": p.Side, "sessionId": msg.SessionID, "path": p.Path, "display": display, "entries": entries,
	})
}

func (s *Server) fsList(side, sessionID, path string) ([]fsEntry, string, error) {
	switch side {
	case "local":
		list, err := workspace.List(path)
		if err != nil {
			return nil, "", err
		}
		display, _ := workspace.Display(path)
		out := make([]fsEntry, 0, len(list))
		for _, e := range list {
			out = append(out, fsEntry{
				Name: e.Name, Size: e.Size, IsDir: e.IsDir, Mode: e.Mode,
				Time: e.ModTime.Format("2006-01-02 15:04"),
			})
		}
		return out, display, nil
	case "remote":
		ds := s.session(sessionID)
		if ds == nil || ds.sftp == nil || !ds.sftp.IsConnected() {
			return nil, "", fmt.Errorf("远端未连接（请先连接 SSH 会话）")
		}
		list, err := ds.sftp.List(path)
		if err != nil {
			return nil, "", err
		}
		out := make([]fsEntry, 0, len(list))
		for _, e := range list {
			out = append(out, fsEntry{
				Name: e.Name, Size: e.Size, IsDir: e.IsDir, Mode: e.Mode,
				Time: e.ModTime.Format("2006-01-02 15:04"),
			})
		}
		if path == "" {
			path = "/"
		}
		return out, path, nil
	case "serial":
		ds := s.session(sessionID)
		if ds == nil || ds.serial == nil || !ds.serial.IsOpen() {
			return nil, "", fmt.Errorf("串口未打开")
		}
		if path == "" {
			path = "/"
		}
		out, err := s.serialList(ds, path)
		if err != nil {
			return nil, "", err
		}
		return out, path, nil
	default:
		return nil, "", fmt.Errorf("未知文件区域: %s", side)
	}
}

// serialList scrapes `ls -la` from a device console (best effort).
func (s *Server) serialList(ds *deviceSession, path string) ([]fsEntry, error) {
	out, err := ds.serial.RunCaptureSilent("ls -la "+shellQuote(path), 450*time.Millisecond, 6*time.Second)
	if err != nil {
		return nil, err
	}
	list := serial.ParseLS(out)
	entries := make([]fsEntry, 0, len(list))
	for _, e := range list {
		entries = append(entries, fsEntry{
			Name: e.Name, Size: e.Size, IsDir: e.IsDir, Mode: e.Mode, Time: e.Time,
		})
	}
	return entries, nil
}

func (s *Server) handleFSMkdir(c *client, msg message) {
	var p struct {
		Side string `json:"side"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return
	}
	if err := s.fsMkdir(p.Side, msg.SessionID, p.Path); err != nil {
		s.sendError(c, err)
		return
	}
	s.sendTo(c, "fs.done", map[string]any{"op": "mkdir", "side": p.Side, "path": p.Path})
}

func (s *Server) fsMkdir(side, sessionID, path string) error {
	switch side {
	case "local":
		return workspace.Mkdir(path)
	case "remote":
		ds := s.session(sessionID)
		if ds == nil || ds.sftp == nil || !ds.sftp.IsConnected() {
			return fmt.Errorf("远端未连接（请先连接 SSH 会话）")
		}
		return ds.sftp.Mkdir(path)
	case "serial":
		ds := s.session(sessionID)
		if ds == nil || ds.serial == nil || !ds.serial.IsOpen() {
			return fmt.Errorf("串口未打开")
		}
		_, err := ds.serial.RunCaptureSilent("mkdir -p "+shellQuote(path), 350*time.Millisecond, 4*time.Second)
		return err
	default:
		return fmt.Errorf("未知文件区域: %s", side)
	}
}

func (s *Server) handleFSNewFile(c *client, msg message) {
	var p struct {
		Side string `json:"side"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return
	}
	if err := s.fsNewFile(p.Side, msg.SessionID, p.Path); err != nil {
		s.sendError(c, err)
		return
	}
	s.sendTo(c, "fs.done", map[string]any{"op": "newfile", "side": p.Side, "path": p.Path})
}

func (s *Server) fsNewFile(side, sessionID, path string) error {
	switch side {
	case "local":
		return workspace.NewFile(path)
	case "remote":
		ds := s.session(sessionID)
		if ds == nil || ds.sftp == nil || !ds.sftp.IsConnected() {
			return fmt.Errorf("远端未连接（请先连接 SSH 会话）")
		}
		return ds.sftp.NewFile(path)
	case "serial":
		ds := s.session(sessionID)
		if ds == nil || ds.serial == nil || !ds.serial.IsOpen() {
			return fmt.Errorf("串口未打开")
		}
		_, err := ds.serial.RunCaptureSilent("touch "+shellQuote(path), 350*time.Millisecond, 4*time.Second)
		return err
	default:
		return fmt.Errorf("未知文件区域: %s", side)
	}
}

// handleFSUpload sends local bytes to a remote directory (local -> remote).
func (s *Server) handleFSUpload(c *client, msg message) {
	ds := s.session(msg.SessionID)
	if ds == nil || ds.sftp == nil || !ds.sftp.IsConnected() {
		s.sendError(c, fmt.Errorf("远端未连接（请先连接 SSH 会话）"))
		return
	}
	var p struct {
		Dir  string `json:"dir"`
		Name string `json:"name"`
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if len(p.Data) > sftpx.MaxTransfer {
		s.sendError(c, fmt.Errorf("文件超过 %d MiB 上限", sftpx.MaxTransfer>>20))
		return
	}
	if p.Name == "" {
		s.sendError(c, fmt.Errorf("缺少文件名"))
		return
	}
	target := path.Join(p.Dir, p.Name)
	if err := ds.sftp.Upload(target, p.Data); err != nil {
		s.sendError(c, err)
		return
	}
	s.sendTo(c, "fs.done", map[string]any{
		"op": "upload", "side": "remote", "sessionId": ds.id, "path": target, "size": len(p.Data),
	})
}

// handleFSDownload pulls a remote file into the local workspace.
func (s *Server) handleFSDownload(c *client, msg message) {
	ds := s.session(msg.SessionID)
	if ds == nil || ds.sftp == nil || !ds.sftp.IsConnected() {
		s.sendError(c, fmt.Errorf("远端未连接（请先连接 SSH 会话）"))
		return
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, err)
		return
	}
	data, err := ds.sftp.Download(p.Path)
	if err != nil {
		s.sendError(c, err)
		return
	}
	name := path.Base(p.Path)
	local, err := workspace.Write(uniqueName(name), data)
	if err != nil {
		s.sendError(c, err)
		return
	}
	s.sendTo(c, "fs.done", map[string]any{
		"op": "download", "side": "local", "sessionId": ds.id,
		"path": local, "remote": p.Path, "size": len(data),
	})
}

func (s *Server) handleFSDelete(c *client, msg message) {
	var p struct {
		Side string `json:"side"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if err := s.fsDelete(p.Side, msg.SessionID, p.Path); err != nil {
		s.sendError(c, err)
		return
	}
	s.sendTo(c, "fs.done", map[string]any{"op": "delete", "side": p.Side, "path": p.Path})
}

func (s *Server) fsDelete(side, sessionID, path string) error {
	switch side {
	case "local":
		return workspace.Delete(path)
	case "remote":
		ds := s.session(sessionID)
		if ds == nil || ds.sftp == nil || !ds.sftp.IsConnected() {
			return fmt.Errorf("远端未连接（请先连接 SSH 会话）")
		}
		return ds.sftp.Delete(path)
	case "serial":
		ds := s.session(sessionID)
		if ds == nil || ds.serial == nil || !ds.serial.IsOpen() {
			return fmt.Errorf("串口未打开")
		}
		_, err := ds.serial.RunCaptureSilent("rm -rf "+shellQuote(path), 350*time.Millisecond, 5*time.Second)
		return err
	default:
		return fmt.Errorf("未知文件区域: %s", side)
	}
}

// handleFSRead returns a text file's content (local / remote only).
func (s *Server) handleFSRead(c *client, msg message) {
	var p struct {
		Side string `json:"side"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if p.Side == "serial" {
		s.sendError(c, fmt.Errorf("串口模式暂不支持在线编辑"))
		return
	}
	var (
		data []byte
		err  error
	)
	if p.Side == "local" {
		data, err = workspace.Read(p.Path)
	} else {
		ds := s.session(msg.SessionID)
		if ds == nil || ds.sftp == nil || !ds.sftp.IsConnected() {
			err = fmt.Errorf("远端未连接（请先连接 SSH 会话）")
		} else {
			data, err = ds.sftp.Download(p.Path)
		}
	}
	if err != nil {
		s.sendError(c, err)
		return
	}
	s.sendTo(c, "fs.content", map[string]any{
		"side": p.Side, "path": p.Path, "data": data, "size": len(data),
	})
}

func (s *Server) handleFSWrite(c *client, msg message) {
	var p struct {
		Side string `json:"side"`
		Path string `json:"path"`
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if len(p.Data) > sftpx.MaxTransfer {
		s.sendError(c, fmt.Errorf("内容超过 %d MiB 上限", sftpx.MaxTransfer>>20))
		return
	}
	switch p.Side {
	case "local":
		if _, err := workspace.Write(p.Path, p.Data); err != nil {
			s.sendError(c, err)
			return
		}
	case "remote":
		ds := s.session(msg.SessionID)
		if ds == nil || ds.sftp == nil || !ds.sftp.IsConnected() {
			s.sendError(c, fmt.Errorf("远端未连接（请先连接 SSH 会话）"))
			return
		}
		if err := ds.sftp.Upload(p.Path, p.Data); err != nil {
			s.sendError(c, err)
			return
		}
	default:
		s.sendError(c, fmt.Errorf("串口模式暂不支持在线编辑"))
		return
	}
	s.sendTo(c, "fs.done", map[string]any{
		"op": "write", "side": p.Side, "path": p.Path, "size": len(p.Data),
	})
}

// uniqueName returns a workspace-relative file name that does not yet exist.
func uniqueName(name string) string {
	if name == "" || name == "." || name == "/" {
		name = "download.bin"
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := name
	for i := 1; ; i++ {
		if _, err := workspace.Resolve(candidate); err != nil {
			return candidate
		}
		if _, err := workspace.Read(candidate); err != nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// decodeHex accepts hex with optional whitespace, commas and 0x prefixes.
func decodeHex(in string) ([]byte, error) {
	clean := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "", ",", "", "0x", "", "0X", "").Replace(in)
	if len(clean)%2 != 0 {
		return nil, fmt.Errorf("HEX 长度必须为偶数")
	}
	buf, err := hex.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("无效的 HEX 数据: %w", err)
	}
	return buf, nil
}
