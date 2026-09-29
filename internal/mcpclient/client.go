// Package mcpclient is a Model Context Protocol *client* over stdio: it launches
// an external MCP server as a child process, performs the initialize handshake,
// lists its tools and calls them. The host wraps each connected server as an
// external Kit so its tools flow into the same Registry the built-in agent and
// `edgekit mcp` consume — that is how external knowledge bases / RAG services
// attach to EdgeKit.
//
// It mirrors internal/acp's process handling and reuses internal/mcp's JSON-RPC
// framing conventions (newline-delimited over stdio).
package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion is the MCP revision this client requests.
const ProtocolVersion = "2025-06-18"

// ErrClosed is returned when the child process is not running.
var ErrClosed = errors.New("MCP 服务未运行")

// Tool is a tool advertised by a remote MCP server.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

// Options configures a Client.
type Options struct {
	ID      string            // stable id, e.g. "kb"
	Name    string            // display name; falls back to ID
	Command string            // executable
	Args    []string          // arguments
	Env     map[string]string // extra env entries (KEY=VALUE)
	Dir     string            // working directory
	Logf    func(format string, args ...any)
}

// Client speaks MCP to a child server process over stdio.
type Client struct {
	opts Options

	cmd   *exec.Cmd
	stdin io.WriteCloser
	enc   *json.Encoder

	writeMu sync.Mutex

	mu      sync.Mutex
	seq     int64
	pending map[int64]chan rpcResult
	tools   []Tool

	started  atomic.Bool
	closing  atomic.Bool
	done     chan struct{}
	failOnce sync.Once

	ctx    context.Context
	cancel context.CancelFunc
}

type rpcResult struct {
	result json.RawMessage
	err    error
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

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type inbound struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// New creates a client for the given command; call Start to launch it.
func New(opts Options) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		opts:    opts,
		pending: make(map[int64]chan rpcResult),
		done:    make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start launches the child, performs the initialize handshake and fetches the
// tool list. The caller's ctx bounds the handshake only; the process lives on.
func (c *Client) Start(ctx context.Context) error {
	if c.started.Load() {
		return errors.New("MCP 服务已启动")
	}
	if strings.TrimSpace(c.opts.Command) == "" {
		return errors.New("未配置 MCP 命令")
	}
	cmd := exec.Command(c.opts.Command, c.opts.Args...)
	cmd.Dir = c.opts.Dir
	env := os.Environ()
	for k, v := range c.opts.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

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
	if err := c.attach(ctx, stdout, stdin, stderr); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

// StartWith attaches explicit stdio pipes instead of spawning a process, then
// runs the same handshake. It lets tests (and future transports) drive the
// client against an in-process MCP server. in is what the client reads;
// out is where the client writes.
func (c *Client) StartWith(ctx context.Context, in io.Reader, out io.WriteCloser) error {
	if c.started.Load() {
		return errors.New("MCP 服务已启动")
	}
	return c.attach(ctx, in, out, nil)
}

// attach wires the pipes, starts the read loops and performs the handshake.
func (c *Client) attach(ctx context.Context, stdout io.Reader, stdin io.WriteCloser, stderr io.Reader) error {
	c.stdin = stdin
	c.enc = json.NewEncoder(stdin)
	c.started.Store(true)

	go c.readLoop(stdout)
	if stderr != nil {
		go c.readStderr(stderr)
	}
	if c.cmd != nil {
		go c.waitLoop()
	}

	if err := c.initialize(ctx); err != nil {
		_ = c.Close()
		return err
	}
	if _, err := c.listTools(ctx); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

func (c *Client) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "edgekit", "version": "0.1.0"},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("MCP initialize: %w", err)
	}
	// The notification needs no reply.
	_ = c.notify("notifications/initialized", nil)
	return nil
}

// listTools fetches and caches tools/list; returns the sorted list.
func (c *Client) listTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("MCP tools/list: %w", err)
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 tools/list: %w", err)
	}
	if out.Tools == nil {
		out.Tools = []Tool{}
	}
	sort.Slice(out.Tools, func(i, j int) bool { return out.Tools[i].Name < out.Tools[j].Name })
	c.mu.Lock()
	c.tools = out.Tools
	c.mu.Unlock()
	return out.Tools, nil
}

// Tools returns the cached tool list.
func (c *Client) Tools() []Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Tool(nil), c.tools...)
}

// Call invokes a remote tool and returns its concatenated text content.
func (c *Client) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	} else {
		params["arguments"] = map[string]any{}
	}
	raw, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析 tools/call: %w", err)
	}
	var b strings.Builder
	for _, part := range out.Content {
		if part.Type == "text" || part.Type == "" {
			b.WriteString(part.Text)
		}
	}
	text := strings.TrimSpace(b.String())
	if out.IsError {
		if text == "" {
			text = "工具返回错误"
		}
		return "", errors.New(text)
	}
	return text, nil
}

// Alive reports whether the child process is running.
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

// Done closes when the child exits (unexpectedly or via Close).
func (c *Client) Done() <-chan struct{} { return c.done }

// Close shuts the child down, escalating to kill if it does not exit.
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
	ch := make(chan rpcResult, 1)
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
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.result, r.err
	case <-timer.C:
		return nil, fmt.Errorf("等待 %s 超时", method)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrClosed
	}
}

func (c *Client) notify(method string, params any) error {
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
			if !errors.Is(err, io.EOF) && c.opts.Logf != nil {
				c.opts.Logf("读取 MCP 输出: %v", err)
			}
			c.finish()
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
		c.opts.Logf("[mcp %s] %s", c.opts.ID, sc.Text())
	}
}

func (c *Client) waitLoop() {
	err := c.cmd.Wait()
	if c.closing.Load() {
		c.finish()
		return
	}
	if c.opts.Logf != nil {
		c.opts.Logf("MCP 服务退出: %v", err)
	}
	c.finish()
}

func (c *Client) dispatch(m inbound) {
	switch {
	case m.ID != nil && (m.Result != nil || m.Error != nil):
		c.handleResponse(m)
	case m.Method != "":
		// Server -> client requests (e.g. tools/list_changed) are ignored
		// here; the connector manager re-lists on its own schedule/change.
		if c.opts.Logf != nil && m.ID == nil {
			c.opts.Logf("MCP 通知: %s", m.Method)
		}
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
		ch <- rpcResult{err: fmt.Errorf("%w", m.Error)}
		return
	}
	ch <- rpcResult{result: m.Result}
}

// finish closes done once and rejects pending calls.
func (c *Client) finish() {
	c.failOnce.Do(func() {
		c.cancel()
		c.mu.Lock()
		for id, ch := range c.pending {
			delete(c.pending, id)
			ch <- rpcResult{err: ErrClosed}
		}
		c.mu.Unlock()
		close(c.done)
	})
}
