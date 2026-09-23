package kits

import (
	"context"
	"fmt"

	"edgekit/internal/kit"
)

// sshKit exposes the focused SSH session.
type sshKit struct{ s kit.SSH }

func (sshKit) Manifest() kit.Manifest {
	return kit.Manifest{
		ID:          "edgekit.kit.ssh",
		Name:        "SSH",
		Version:     "0.1.0",
		License:     "Apache-2.0",
		Runtime:     "builtin",
		Activation:  []string{"onStartup", "onDeviceKind:ssh"},
		Description: "SSH 连接状态查询与远端命令执行",
	}
}

func (k sshKit) Tools() []kit.Tool {
	return []kit.Tool{
		{
			Name:        "ssh_status",
			Description: "查询 SSH 会话是否已连接及其目标",
			Risk:        kit.RiskRead,
			Schema:      obj(nil),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.s == nil || !k.s.IsConnected() {
					return "SSH 未连接", nil
				}
				return "SSH 已连接: " + k.s.Target(), nil
			},
		},
		{
			Name:        "ssh_exec",
			Description: "在 Remote（远端设备）上通过 SSH 执行一条 shell 命令并返回输出",
			Risk:        kit.RiskMutate,
			Schema:      obj(map[string]any{"command": strType()}, "command"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.s == nil || !k.s.IsConnected() {
					return "", fmt.Errorf("SSH 未连接")
				}
				return k.s.ExecCapture(argString(args, "command"), 64*1024)
			},
		},
	}
}
