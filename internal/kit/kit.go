// Package kit defines EdgeKit's extension model.
//
// A Kit is a capability pack that contributes tools to the host. Built-in kits
// ship inside the binary; external kits will attach over MCP later. The host
// aggregates every activated kit into a Registry, which the built-in agent and
// (in a later phase) the MCP server both consume — so a capability is defined
// exactly once.
package kit

import (
	"context"
	"sort"
	"sync"
	"time"

	"edgekit/internal/sftpx"
)

// Risk classifies what a tool does, so the host can gate it. The built-in agent
// asks for approval on anything that is not RiskRead.
type Risk string

const (
	RiskRead      Risk = "read"
	RiskMutate    Risk = "mutate"
	RiskDangerous Risk = "dangerous"
)

// Tool is a capability contributed by a kit.
type Tool struct {
	Name        string
	Description string
	Risk        Risk
	Schema      map[string]any // JSON Schema for the arguments
	Call        func(ctx context.Context, args map[string]any) (string, error)
}

// Mutating reports whether the tool changes device or host state.
func (t Tool) Mutating() bool { return t.Risk != RiskRead }

// Manifest describes a kit. It mirrors an editor extension manifest so the same
// shape can later be loaded from a kit.json for external kits.
type Manifest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	License     string   `json:"license,omitempty"`
	Runtime     string   `json:"runtime"`              // "builtin" (or "mcp" later)
	Activation  []string `json:"activation,omitempty"` // onStartup, onDeviceKind:serial, ...
	Description string   `json:"description,omitempty"`
}

// Kit is a capability pack.
type Kit interface {
	Manifest() Manifest
	Tools() []Tool
}

/* ------------------------------------------------------------------ *
 * capabilities the built-in kits operate on
 *
 * They are implemented by the host's focused-session proxies, so a tool call
 * always resolves the currently selected device.
 * ------------------------------------------------------------------ */

// Serial is the serial capability.
type Serial interface {
	IsOpen() bool
	Port() string
	Write(p []byte) error
	Recent() []byte
	RunCapture(command string, quiet, timeout time.Duration) (string, error)
}

// SSH is the SSH capability.
type SSH interface {
	IsConnected() bool
	Target() string
	ExecCapture(command string, maxBytes int) (string, error)
}

// SFTP is the SFTP capability.
type SFTP interface {
	IsConnected() bool
	List(path string) ([]sftpx.Entry, error)
	Download(path string) ([]byte, error)
	Upload(path string, data []byte) error
}

// Deps bundles the capabilities handed to the built-in kits.
type Deps struct {
	Serial Serial
	SSH    SSH
	SFTP   SFTP
}

/* ------------------------------------------------------------------ *
 * registry
 * ------------------------------------------------------------------ */

// entry is a registered kit and whether it is activated.
type entry struct {
	kit     Kit
	enabled bool
}

type toolEntry struct {
	tool  Tool
	kitID string
}

// Registry aggregates the tools contributed by the activated kits.
//
// A kit can be enabled or disabled (like an editor extension): a disabled kit's
// tools are neither advertised to a brain nor executable, but its manifest is
// still listed so the UI can offer to turn it back on.
type Registry struct {
	mu    sync.Mutex
	kits  []*entry
	tools map[string]toolEntry
	order []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]toolEntry)}
}

// Register adds a kit's contributions (enabled by default). A tool name already
// registered by an earlier kit is not overwritten.
func (r *Registry) Register(k Kit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kits = append(r.kits, &entry{kit: k, enabled: true})
	id := k.Manifest().ID
	for _, t := range k.Tools() {
		if _, exists := r.tools[t.Name]; exists {
			continue
		}
		r.tools[t.Name] = toolEntry{tool: t, kitID: id}
		r.order = append(r.order, t.Name)
	}
}

// SetEnabled activates or deactivates a kit. It reports whether the kit exists.
func (r *Registry) SetEnabled(id string, on bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.kits {
		if e.kit.Manifest().ID == id {
			e.enabled = on
			return true
		}
	}
	return false
}

// IsEnabled reports whether a kit is activated.
func (r *Registry) IsEnabled(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.kits {
		if e.kit.Manifest().ID == id {
			return e.enabled
		}
	}
	return false
}

// ToolKit returns the id of the kit that contributed a tool.
func (r *Registry) ToolKit(name string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	te, ok := r.tools[name]
	return te.kitID, ok
}

// Tools returns the tools of the enabled kits, in registration order.
func (r *Registry) Tools() []Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.toolsLocked()
}

// Tool looks up an enabled tool by name.
func (r *Registry) Tool(name string) (Tool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	te, ok := r.tools[name]
	if !ok || !r.enabledLocked(te.kitID) {
		return Tool{}, false
	}
	return te.tool, true
}

// KitTools returns every tool contributed by one kit (regardless of state).
func (r *Registry) KitTools(id string) []Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Tool{}
	for _, name := range r.order {
		if te := r.tools[name]; te.kitID == id {
			out = append(out, te.tool)
		}
	}
	return out
}

// Manifests returns the manifests of the registered kits, sorted by id.
func (r *Registry) Manifests() []Manifest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Manifest, 0, len(r.kits))
	for _, e := range r.kits {
		out = append(out, e.kit.Manifest())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) toolsLocked() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		te := r.tools[name]
		if r.enabledLocked(te.kitID) {
			out = append(out, te.tool)
		}
	}
	return out
}

func (r *Registry) enabledLocked(id string) bool {
	for _, e := range r.kits {
		if e.kit.Manifest().ID == id {
			return e.enabled
		}
	}
	return false
}

/* ------------------------------------------------------------------ *
 * shared helpers
 * ------------------------------------------------------------------ */

// Truncate shortens s to at most n bytes, marking the cut.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(已截断)"
}
