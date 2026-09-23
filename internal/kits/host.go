package kits

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"edgekit/internal/kit"
	"edgekit/internal/netdiag"
)

// hostKit exposes the machine running EdgeKit (the "Local" target).
type hostKit struct{}

func (hostKit) Manifest() kit.Manifest {
	return kit.Manifest{
		ID:          "edgekit.kit.host",
		Name:        "Host",
		Version:     "0.1.0",
		License:     "Apache-2.0",
		Runtime:     "builtin",
		Activation:  []string{"onStartup"},
		Description: "在运行 EdgeKit 的本机（Local）上查询信息、执行命令",
	}
}

func (hostKit) Tools() []kit.Tool {
	return []kit.Tool{
		{
			Name:        "local_info",
			Description: "获取 Local（运行 EdgeKit 的本机）的主机名、系统、CPU 等信息",
			Risk:        kit.RiskRead,
			Schema:      obj(nil),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				return netdiag.SysInfo(), nil
			},
		},
		{
			Name:        "local_exec",
			Description: "在 Local（运行 EdgeKit 的本机）上执行 shell 命令并返回输出",
			Risk:        kit.RiskMutate,
			Schema:      obj(map[string]any{"command": strType()}, "command"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				return hostExec(ctx, argString(args, "command"), 64*1024)
			},
		},
	}
}

// hostExec runs a command on the local machine.
func hostExec(ctx context.Context, command string, maxBytes int) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("命令为空")
	}
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	buf := &capBuffer{max: maxBytes}
	cmd.Stdout = buf
	cmd.Stderr = buf
	runErr := cmd.Run()
	out := buf.String()
	if runErr != nil {
		if out == "" {
			return "", fmt.Errorf("命令执行失败: %w", runErr)
		}
		out += "\n(exit: " + runErr.Error() + ")"
	}
	return out, nil
}

// capBuffer collects at most max bytes.
type capBuffer struct {
	max       int
	buf       []byte
	truncated bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if room := b.max - len(b.buf); room > 0 {
		n := room
		if n > len(p) {
			n = len(p)
		}
		b.buf = append(b.buf, p[:n]...)
		if n < len(p) {
			b.truncated = true
		}
	} else {
		b.truncated = true
	}
	return len(p), nil
}

func (b *capBuffer) String() string {
	s := string(b.buf)
	if b.truncated {
		s += "\n…(输出已截断)"
	}
	return s
}
