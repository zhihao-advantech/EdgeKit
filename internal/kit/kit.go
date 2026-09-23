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

// Registry aggregates the tools contributed by the activated kits.
type Registry struct {
	kits  []Kit
	tools map[string]Tool
	order []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a kit's contributions. A tool name already registered by an
// earlier kit is not overwritten.
func (r *Registry) Register(k Kit) {
	r.kits = append(r.kits, k)
	for _, t := range k.Tools() {
		if _, exists := r.tools[t.Name]; exists {
			continue
		}
		r.tools[t.Name] = t
		r.order = append(r.order, t.Name)
	}
}

// Tools returns every contributed tool, in registration order.
func (r *Registry) Tools() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Tool looks a tool up by name.
func (r *Registry) Tool(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Manifests returns the manifests of the registered kits, sorted by id.
func (r *Registry) Manifests() []Manifest {
	out := make([]Manifest, 0, len(r.kits))
	for _, k := range r.kits {
		out = append(out, k.Manifest())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
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
