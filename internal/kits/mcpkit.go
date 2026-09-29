package kits

import (
	"context"
	"fmt"
	"strings"

	"edgekit/internal/kit"
	"edgekit/internal/mcpclient"
)

// MCPSource is the connected-client surface an mcpKit needs. *mcpclient.Client
// satisfies it; a fake is used in tests.
type MCPSource interface {
	Tools() []mcpclient.Tool
	Call(ctx context.Context, name string, args map[string]any) (string, error)
	Alive() bool
}

// mcpKit adapts a connected external MCP server to a kit. Its tools are
// namespaced by the connector id so they cannot collide with built-in tools or
// another provider, and are routed back to the remote process on call.
//
// External tools are untrusted: their risk defaults to mutate (approval-gated)
// unless the connector config declares them read-only, and their complex schemas
// are not deeply validated here — the remote server is responsible.
type mcpKit struct {
	id       string // registry kit id, e.g. "edgekit.connector.kb"
	name     string
	provider string // namespace prefix, e.g. "kb"
	risk     kit.Risk
	client   MCPSource
}

// NewMCPKit builds an external kit for a connected client.
func NewMCPKit(providerID, displayName string, client MCPSource, risk kit.Risk) kit.Kit {
	if risk != kit.RiskRead {
		risk = kit.RiskMutate
	}
	return mcpKit{
		id:       "edgekit.connector." + providerID,
		name:     displayName,
		provider: providerID,
		risk:     risk,
		client:   client,
	}
}

func (k mcpKit) Manifest() kit.Manifest {
	desc := fmt.Sprintf("外部 MCP 连接器：%s", k.name)
	return kit.Manifest{
		ID:          k.id,
		Name:        k.name,
		Version:     "external",
		License:     "external",
		Runtime:     "mcp",
		Activation:  []string{kit.EventStartup},
		Description: desc,
	}
}

func (k mcpKit) Tools() []kit.Tool {
	remote := k.client.Tools()
	out := make([]kit.Tool, 0, len(remote))
	for _, t := range remote {
		toolName := kit.Namespace(k.provider, t.Name)
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		remoteName := t.Name
		out = append(out, kit.Tool{
			Name:        toolName,
			Description: strings.TrimSpace(t.Description + " （外部 MCP 工具：" + remoteName + "）"),
			Risk:        k.risk,
			Schema:      schema,
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if !k.client.Alive() {
					return "", fmt.Errorf("连接器 %s 未运行", k.provider)
				}
				text, err := k.client.Call(ctx, remoteName, args)
				if err != nil {
					return "", fmt.Errorf("%s: %w", k.name, err)
				}
				return kit.Truncate(text, 32*1024), nil
			},
		})
	}
	return out
}
