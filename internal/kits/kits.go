// Package kits holds EdgeKit's built-in capability kits: host, net, serial,
// ssh, sftp and workspace. Each kit contributes tools to the host registry.
//
// These definitions used to live in internal/agent; they were moved here so a
// capability is declared once and can be consumed by the built-in agent, the
// UI protocol and (later) an MCP server.
package kits

import (
	"strconv"

	"edgekit/internal/kit"
)

// Builtin returns the built-in kits wired to the given capabilities.
func Builtin(deps kit.Deps) []kit.Kit {
	return []kit.Kit{
		hostKit{},
		netKit{},
		serialKit{deps.Serial},
		sshKit{deps.SSH},
		sftpKit{deps.SFTP},
		workspaceKit{},
	}
}

/* ---- JSON Schema helpers ---- */

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func strType() map[string]any { return map[string]any{"type": "string"} }
func intType() map[string]any { return map[string]any{"type": "integer"} }

/* ---- argument helpers ---- */

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
