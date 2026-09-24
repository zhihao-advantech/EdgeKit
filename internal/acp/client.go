package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed is returned when the agent process is not running.
var ErrClosed = errors.New("agent 进程未运行")

// Options configures a Client.
type Options struct {
	Command string   // executable, e.g. "hermes" or "openclaw"
	Args    []string // e.g. ["acp"]
	Env     []string // extra KEY=VALUE entries appended to the host environment
	Dir     string   // working directory for the child process

	// OnUpdate receives every session/update notification.
	OnUpdate func(Update)
	// OnPermission is called (on a worker goroutine) when the agent asks for
	// approval. It must block until the user decides. A nil handler cancels.
	OnPermission func(ctx context.Context, req PermissionRequest) PermissionOutcome
	// OnExit is called once if the process exits unexpectedly (not via Close).
	OnExit func(error)
	// Logf receives child stderr lines. Nil discards them.
	Logf func(format string, args ...any)
}

// Client speaks ACP to a child agent process over stdio.
type Client struct {
	opts Options

	cmd   *exec.Cmd
	stdin io.WriteCloser
	enc   *json.Encoder

	writeMu sync.Mutex

	mu      sync.Mutex
	seq     int64
	pending map[int64]chan rpcResponse

	started  atomic.Bool
	closing  atomic.Bool
	done     chan struct{}
	failOnce sync.Once

	ctx    context.Context
	cancel context.CancelFunc
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("ACP 错误 %d: %s", e.Code, e.Message)
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type inbound struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

// New creates a client for the given command. Call Start to launch it.
func New(opts Options) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		opts:    opts,
		pending: make(map[int64]chan rpcResponse),
		done:    make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start launches the child process, begins reading its output and performs the
// ACP initialize handshake.
func (c *Client) Start(ctx context.Context) error {
	if c.started.Load() {
		return errors.New("agent 已启动")
	}
	if strings.TrimSpace(c.opts.Command) == "" {
		return errors.New("未配置 ACP 命令")
	}
	cmd := exec.Command(c.opts.Command, c.opts.Args...)
	cmd.Dir = c.opts.Dir
	cmd.Env = append(os.Environ(), c.opts.Env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 %s: %w", c.opts.Command, err)
	}

	c.cmd = cmd
	c.stdin = stdin
	c.enc = json.NewEncoder(stdin)
	c.started.Store(true)

	go c.readLoop(stdout)
	go c.readStderr(stderr)
	go c.waitLoop()

	return c.Initialize(ctx)
}

// Alive reports whether the process is running.
func (c *Client) Alive() bool {
	if !c.started.Load() {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// Initialize performs the ACP handshake.
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion":    ProtocolVersion,
		"clientCapabilities": map[string]any{},
		"clientInfo":         Implementation{Name: "edgekit", Version: ClientVersion},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("ACP initialize: %w", err)
	}
	return nil
}

// NewSession creates a session and hands the agent the given MCP servers.
func (c *Client) NewSession(ctx context.Context, cwd string, servers []MCPServer) (Session, error) {
	params := map[string]any{"cwd": cwd, "mcpServers": normalizeServers(servers)}
	raw, err := c.call(ctx, "session/new", params)
	if err != nil {
		return Session{}, err
	}
	return parseSession(raw)
}

// LoadSession resumes a persisted session. The agent replays the prior
// conversation through session/update notifications before it responds.
func (c *Client) LoadSession(ctx context.Context, cwd, sessionID string, servers []MCPServer) (Session, error) {
	params := map[string]any{
		"sessionId":  sessionID,
		"cwd":        cwd,
		"mcpServers": normalizeServers(servers),
	}
	raw, err := c.call(ctx, "session/load", params)
	if err != nil {
		return Session{}, err
	}
	// session/load's response carries models/modes but not the session id.
	s := Session{ID: sessionID}
	models, current, err := decodeModelState(raw)
	if err != nil {
		return Session{}, err
	}
	s.Models, s.CurrentModelID = models, current
	return s, nil
}

// ListSessions returns the agent's persisted sessions (optionally by cwd).
func (c *Client) ListSessions(ctx context.Context, cwd string) ([]SessionInfo, error) {
	params := map[string]any{}
	if cwd != "" {
		params["cwd"] = cwd
	}
	raw, err := c.call(ctx, "session/list", params)
	if err != nil {
		return nil, err
	}
	var out struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 session/list 响应: %w", err)
	}
	if out.Sessions == nil {
		out.Sessions = []SessionInfo{}
	}
	return out.Sessions, nil
}

func normalizeServers(servers []MCPServer) []MCPServer {
	norm := make([]MCPServer, 0, len(servers))
	for _, s := range servers {
		if s.Args == nil {
			s.Args = []string{}
		}
		if s.Env == nil {
			s.Env = []EnvVar{}
		}
		norm = append(norm, s)
	}
	return norm
}

func parseSession(raw json.RawMessage) (Session, error) {
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Session{}, fmt.Errorf("解析会话响应: %w", err)
	}
	if out.SessionID == "" {
		return Session{}, errors.New("会话响应未返回 sessionId")
	}
	s := Session{ID: out.SessionID}
	models, current, err := decodeModelState(raw)
	if err != nil {
		return Session{}, err
	}
	s.Models, s.CurrentModelID = models, current
	return s, nil
}

// decodeModelState pulls the optional models block out of a session response.
func decodeModelState(raw json.RawMessage) ([]ModelInfo, string, error) {
	var out struct {
		Models *struct {
			AvailableModels []ModelInfo `json:"availableModels"`
			CurrentModelID  string      `json:"currentModelId"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "", fmt.Errorf("解析会话响应: %w", err)
	}
	if out.Models == nil {
		return nil, "", nil
	}
	return out.Models.AvailableModels, out.Models.CurrentModelID, nil
}

// SetModel switches the model for a session (ACP session/set_model).
func (c *Client) SetModel(ctx context.Context, sessionID, modelID string) error {
	params := map[string]any{"sessionId": sessionID, "modelId": modelID}
	if _, err := c.call(ctx, "session/set_model", params); err != nil {
		return err
	}
	return nil
}

// Prompt sends a user message and blocks until the turn ends. It returns the
// agent's stopReason.
func (c *Client) Prompt(ctx context.Context, sessionID, text string) (string, error) {
	params := map[string]any{
		"sessionId": sessionID,
		"prompt":    []TextContent{{Type: "text", Text: text}},
	}
	raw, err := c.call(ctx, "session/prompt", params)
	if err != nil {
		return "", err
	}
	var out struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.StopReason, nil
}

// Cancel asks the agent to stop the running turn.
func (c *Client) Cancel(ctx context.Context, sessionID string) error {
	return c.notify(ctx, "session/cancel", map[string]any{"sessionId": sessionID})
}

// Close shuts the child process down, escalating to kill if it does not exit.
func (c *Client) Close() error {
	c.closing.Store(true)
	c.cancel()
	if !c.started.Load() {
		return nil
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.done
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&c.seq, 1)
	ch := make(chan rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.write(request{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return r.result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrClosed
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.write(notification{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Client) write(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	return c.enc.Encode(v)
}

func (c *Client) readLoop(stdout io.Reader) {
	dec := json.NewDecoder(stdout)
	for {
		var m inbound
		if err := dec.Decode(&m); err != nil {
			if !errors.Is(err, io.EOF) {
				c.fail(fmt.Errorf("读取 ACP 输出: %w", err))
			}
			return
		}
		c.dispatch(m)
	}
}

func (c *Client) readStderr(stderr io.Reader) {
	if c.opts.Logf == nil {
		_, _ = io.Copy(io.Discard, stderr)
		return
	}
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		c.opts.Logf("%s", sc.Text())
	}
}

func (c *Client) waitLoop() {
	err := c.cmd.Wait()
	if c.closing.Load() {
		c.finish()
		return
	}
	c.fail(fmt.Errorf("agent 进程已退出: %v", err))
}

func (c *Client) dispatch(m inbound) {
	switch {
	case m.Method != "" && len(m.ID) > 0:
		go c.handleRequest(m)
	case m.Method != "":
		c.handleNotification(m)
	case len(m.ID) > 0:
		c.handleResponse(m)
	}
}

func (c *Client) handleResponse(m inbound) {
	var id int64
	if err := json.Unmarshal(m.ID, &id); err != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	c.mu.Unlock()
	if ch == nil {
		return
	}
	if m.Error != nil {
		ch <- rpcResponse{err: m.Error}
		return
	}
	ch <- rpcResponse{result: m.Result}
}

func (c *Client) handleRequest(m inbound) {
	switch m.Method {
	case "session/request_permission":
		var req PermissionRequest
		if err := json.Unmarshal(m.Params, &req); err != nil {
			c.replyError(m.ID, -32602, "invalid params: "+err.Error())
			return
		}
		outcome := Cancelled()
		if c.opts.OnPermission != nil {
			outcome = c.opts.OnPermission(c.ctx, req)
		}
		// ACP expects the decision wrapped in an outer "outcome" object:
		//   {"outcome":{"outcome":"selected","optionId":"allow_once"}}
		//   {"outcome":{"outcome":"cancelled"}}
		c.reply(m.ID, map[string]any{"outcome": outcome})
	default:
		c.replyError(m.ID, -32601, "method not found: "+m.Method)
	}
}

func (c *Client) handleNotification(m inbound) {
	if m.Method != "session/update" {
		return
	}
	// ACP wraps the update: params = {"sessionId": "...", "update": {...}}.
	var n struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(m.Params, &n); err != nil || len(n.Update) == 0 {
		return
	}
	var u Update
	if err := json.Unmarshal(n.Update, &u); err != nil {
		return
	}
	if u.SessionID == "" {
		u.SessionID = n.SessionID
	}
	if c.opts.OnUpdate != nil {
		c.opts.OnUpdate(u)
	}
}

func (c *Client) reply(id json.RawMessage, result any) {
	_ = c.write(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (c *Client) replyError(id json.RawMessage, code int, msg string) {
	_ = c.write(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

// fail tears the client down once, rejecting pending calls and reporting the
// unexpected exit.
func (c *Client) fail(err error) {
	c.failOnce.Do(func() {
		c.finishLocked(err)
		if c.opts.OnExit != nil {
			c.opts.OnExit(err)
		}
	})
}

// finish closes done and rejects pending calls without reporting an error.
func (c *Client) finish() {
	c.failOnce.Do(func() { c.finishLocked(ErrClosed) })
}

func (c *Client) finishLocked(err error) {
	c.cancel()
	c.mu.Lock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- rpcResponse{err: err}
	}
	c.mu.Unlock()
	close(c.done)
}
