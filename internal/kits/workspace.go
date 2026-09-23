package kits

import (
	"context"
	"fmt"
	"strings"

	"edgekit/internal/kit"
	"edgekit/internal/workspace"
)

// workspaceKit exposes the sandboxed local working directory.
type workspaceKit struct{}

func (workspaceKit) Manifest() kit.Manifest {
	return kit.Manifest{
		ID:          "edgekit.kit.workspace",
		Name:        "Workspace",
		Version:     "0.1.0",
		License:     "Apache-2.0",
		Runtime:     "builtin",
		Activation:  []string{"onStartup"},
		Description: "本地工作区（固件/配置/脚本暂存）的读写",
	}
}

func (workspaceKit) Tools() []kit.Tool {
	return []kit.Tool{
		{
			Name:        "workspace_list",
			Description: "列出本地工作区目录内容",
			Risk:        kit.RiskRead,
			Schema:      obj(map[string]any{"path": strType()}),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				list, err := workspace.List(argString(args, "path"))
				if err != nil {
					return "", err
				}
				if len(list) == 0 {
					return "(空目录)", nil
				}
				var b strings.Builder
				for _, e := range list {
					kind := "file"
					if e.IsDir {
						kind = "dir "
					}
					fmt.Fprintf(&b, "%s %10d  %s\n", kind, e.Size, e.Name)
				}
				return b.String(), nil
			},
		},
		{
			Name:        "workspace_read",
			Description: "读取本地工作区中的文本文件",
			Risk:        kit.RiskRead,
			Schema:      obj(map[string]any{"path": strType()}, "path"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				data, err := workspace.Read(argString(args, "path"))
				if err != nil {
					return "", err
				}
				return kit.Truncate(strings.ToValidUTF8(string(data), "\uFFFD"), 16*1024), nil
			},
		},
		{
			Name:        "workspace_write",
			Description: "在本地工作区写入文本文件（固件、配置、脚本等）",
			Risk:        kit.RiskMutate,
			Schema:      obj(map[string]any{"path": strType(), "content": strType()}, "path", "content"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				local, err := workspace.Write(argString(args, "path"), []byte(argString(args, "content")))
				if err != nil {
					return "", err
				}
				return "已写入 " + local, nil
			},
		},
	}
}
