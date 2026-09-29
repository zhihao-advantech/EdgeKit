package kits

import (
	"context"
	"fmt"
	"strings"
	"time"

	"edgekit/internal/kit"
	"edgekit/internal/workspace"
)

// codeEditKit contributes patch-based code editing tools for the closed-loop
// debugging workflow: the agent proposes a change (with hypothesis and verify
// command), the user reviews it in an experiment card, then it is applied.
type codeEditKit struct {
	sftp kit.SFTP
	ssh  kit.SSH
}

func (codeEditKit) Manifest() kit.Manifest {
	return kit.Manifest{
		ID:          "edgekit.kit.code",
		Name:        "CodeEdit",
		Version:     "0.1.0",
		License:     "Apache-2.0",
		Runtime:     "builtin",
		Activation:  []string{"onStartup"},
		Description: "代码编辑、补丁应用与实验验证（闭环调试）",
	}
}

func (k codeEditKit) Tools() []kit.Tool {
	return []kit.Tool{
		{
			Name:        "code_patch",
			Description: "应用代码补丁到工作区文件。请务必提供 diff（unified diff 格式：-/+/空格 行），以便用户在审批卡片中审查变更。",
			Risk:        kit.RiskMutate,
			Schema: obj(map[string]any{
				"path":       strType(),
				"content":    strType(),
				"hypothesis": strType(),
				"verify":     strType(),
				"diff":       strType(),
			}, "path", "content"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				path := argString(args, "path")
				content := argString(args, "content")
				hypothesis := argString(args, "hypothesis")
				verify := argString(args, "verify")

				if path == "" {
					return "", fmt.Errorf("path 不能为空")
				}

				diff, err := workspace.ApplyPatch(path, content)
				if err != nil {
					return "", err
				}

				var sb strings.Builder
				fmt.Fprintf(&sb, "已应用补丁到 %s\n", path)
				if hypothesis != "" {
					fmt.Fprintf(&sb, "假设：%s\n", hypothesis)
				}
				if verify != "" {
					fmt.Fprintf(&sb, "验证：%s\n", verify)
				}
				fmt.Fprintf(&sb, "diff:\n%s", diff)
				return sb.String(), nil
			},
		},
		{
			Name:        "code_diff",
			Description: "显示文件变更 diff（只读，用于审查，不写入文件）",
			Risk:        kit.RiskRead,
			Schema: obj(map[string]any{
				"path":    strType(),
				"content": strType(),
			}, "path", "content"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				path := argString(args, "path")
				content := argString(args, "content")

				if path == "" {
					return "", fmt.Errorf("path 不能为空")
				}

				oldContent, _ := workspace.Read(path)
				return workspace.SimpleDiff(string(oldContent), content), nil
			},
		},
		{
			Name:        "code_deploy",
			Description: "推送本地工作区文件到板端（SFTP）",
			Risk:        kit.RiskMutate,
			Schema: deviceSchema(map[string]any{
				"path":   strType(),
				"target": strType(),
			}, "path", "target"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				path := argString(args, "path")
				target := argString(args, "target")
				if path == "" || target == "" {
					return "", fmt.Errorf("path 和 target 不能为空")
				}
				if k.sftp == nil || !k.sftp.IsConnected() {
					return "", fmt.Errorf("SFTP 未连接（请先连接 SSH）")
				}
				data, err := workspace.Read(path)
				if err != nil {
					return "", err
				}
				if err := k.sftp.Upload(ctx, target, data); err != nil {
					return "", err
				}
				return fmt.Sprintf("已部署 %s → %s（%d 字节）", path, target, len(data)), nil
			},
		},
		{
			Name:        "code_run",
			Description: "在板端运行命令并返回结构化结果（exit code + output + timing）",
			Risk:        kit.RiskMutate,
			Schema: deviceSchema(map[string]any{
				"command": strType(),
			}, "command"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				command := argString(args, "command")
				if command == "" {
					return "", fmt.Errorf("command 不能为空")
				}
				if k.ssh == nil || !k.ssh.IsConnected() {
					return "", fmt.Errorf("SSH 未连接")
				}
				start := time.Now()
				out, err := k.ssh.ExecCapture(ctx, command, 64*1024)
				duration := time.Since(start)
				if err != nil {
					return "", err
				}
				exitCode := 0
				if strings.Contains(out, "(exit:") {
					exitCode = 1
				}
				return fmt.Sprintf("exit_code: %d\nduration: %s\noutput:\n%s",
					exitCode, duration.Round(time.Millisecond), out), nil
			},
		},
		{
			Name:        "code_revert",
			Description: "回退文件到写入前的备份（.backup/ 目录）",
			Risk:        kit.RiskMutate,
			Schema: obj(map[string]any{
				"path": strType(),
			}, "path"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				path := argString(args, "path")
				if path == "" {
					return "", fmt.Errorf("path 不能为空")
				}
				return workspace.Revert(path)
			},
		},
	}
}
