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
		codeEditKit{deps.SFTP, deps.SSH},
		timelineKit{deps.Timeline},
		sessionsKit{deps.Sessions},
	}
}

/* ---- JSON Schema helpers ---- */

func obj(props map[string]any, required ...string) map[string]any {
	if props == nil {
		// MCP requires inputSchema.properties to be an object, not null.
		props = map[string]any{}
	}
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func strType() map[string]any { return map[string]any{"type": "string"} }
func intType() map[string]any { return map[string]any{"type": "integer"} }

// deviceObj builds an object schema for a device tool and adds the shared
// `session` argument, which routes the call to a specific device session
// instead of the focused one.
func deviceObj(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	props["session"] = map[string]any{
		"type":        "string",
		"description": "目标设备会话 id（可选，默认当前聚焦会话；可用 sessions_list 查看）",
	}
	return obj(props, required...)
}

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
