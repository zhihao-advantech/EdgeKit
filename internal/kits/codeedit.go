package kits

import (
	"context"
	"fmt"
	"strings"

	"edgekit/internal/kit"
	"edgekit/internal/workspace"
)

// codeEditKit contributes patch-based code editing tools for the closed-loop
// debugging workflow: the agent proposes a change (with hypothesis and verify
// command), the user reviews it in an experiment card, then it is applied.
type codeEditKit struct{}

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

func (codeEditKit) Tools() []kit.Tool {
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
	}
}
