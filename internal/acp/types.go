// Package acp implements a minimal Agent Client Protocol client.
//
// ACP is JSON-RPC 2.0 over stdio (newline-delimited), the same wire style as
// MCP. EdgeKit uses it to drive an external agent — Hermes (`hermes acp`) or
// OpenClaw (`openclaw acp`) — as a child process, exposing EdgeKit's own tools
// to that agent by handing it the `edgekit mcp` server at session creation.
//
// Only the subset EdgeKit needs is modelled: initialize, session/new,
// session/prompt, session/cancel, the session/update notifications and the
// agent→client session/request_permission request. Unknown fields are ignored,
// so a newer agent stays compatible.
package acp

import (
	"encoding/json"
	"strings"
)

// ProtocolVersion is the ACP revision EdgeKit speaks.
const ProtocolVersion = 1

// ClientVersion identifies EdgeKit to the agent. ACP requires clientInfo to
// carry a version, so this is always sent.
const ClientVersion = "0.1.0"

// EnvVar is one environment entry for a stdio MCP server.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MCPServer describes a stdio MCP server handed to the agent at session/new.
// args and env are required by the ACP schema, so they are always serialized
// (as empty arrays rather than omitted).
type MCPServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []EnvVar `json:"env"`
}

// MarshalJSON keeps args/env as [] even when nil, since the ACP schema marks
// them required on stdio MCP servers.
func (s MCPServer) MarshalJSON() ([]byte, error) {
	type alias MCPServer
	a := alias(s)
	if a.Args == nil {
		a.Args = []string{}
	}
	if a.Env == nil {
		a.Env = []EnvVar{}
	}
	return json.Marshal(a)
}

// ModelInfo is one selectable model advertised by the agent.
type ModelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Session is the result of session/new (or session/load).
type Session struct {
	ID             string
	Models         []ModelInfo
	CurrentModelID string
}

// SessionInfo describes one persisted session (session/list).
type SessionInfo struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd,omitempty"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// Implementation identifies a client or agent.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// PermissionOption is one choice in a permission prompt.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // allow_once, allow_always, reject_once, reject_always
}

// ToolCallUpdate is the tool-call view carried by a permission request and by
// tool_call / tool_call_update notifications.
type ToolCallUpdate struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Status     string          `json:"status,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`
}

// PermissionRequest is sent by the agent when a tool call needs approval.
type PermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// PermissionOutcome is the client's answer to a PermissionRequest.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"` // "selected" or "cancelled"
	OptionID string `json:"optionId,omitempty"`
}

// Selected builds an outcome that picks one of the offered options.
func Selected(optionID string) PermissionOutcome {
	return PermissionOutcome{Outcome: "selected", OptionID: optionID}
}

// Cancelled builds an outcome that dismisses the request.
func Cancelled() PermissionOutcome {
	return PermissionOutcome{Outcome: "cancelled"}
}

// TextContent is a text content block (used for prompt input and message
// chunks).
type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Update is one session/update notification.
type Update struct {
	SessionID     string          `json:"sessionId"`
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content,omitempty"`
	MessageID     string          `json:"messageId,omitempty"`

	// Tool call fields (tool_call, tool_call_update).
	ToolCallID string          `json:"toolCallId,omitempty"`
	Title      string          `json:"title,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Status     string          `json:"status,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`
}

// Text returns the text of an agent_message_chunk / user_message_chunk update,
// or "" for other updates.
func (u Update) Text() string {
	switch u.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk", "user_message_chunk":
	default:
		return ""
	}
	if len(u.Content) == 0 {
		return ""
	}
	var c TextContent
	if err := json.Unmarshal(u.Content, &c); err != nil {
		return ""
	}
	return c.Text
}

// ContentText extracts the human-readable text from a tool call's content
// blocks. ACP content is a list of variants; only text-bearing "content"
// blocks are rendered, everything else (diff, terminal) is ignored.
func (u Update) ContentText() string {
	if len(u.Content) == 0 {
		return ""
	}
	var blocks []struct {
		Type    string `json:"type"`
		Content *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(u.Content, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Content != nil && b.Content.Text != "" {
			parts = append(parts, b.Content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// SessionUpdate kinds EdgeKit acts on. Other kinds (plan, usage_update,
// available_commands_update, ...) are ignored.
const (
	UpdateAgentMessage   = "agent_message_chunk"
	UpdateToolCall       = "tool_call"
	UpdateToolCallUpdate = "tool_call_update"
)
