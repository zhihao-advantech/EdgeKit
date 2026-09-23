package kits

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"edgekit/internal/kit"
	"edgekit/internal/workspace"
)

// sftpKit exposes file transfer over the focused SSH connection.
type sftpKit struct{ s kit.SFTP }

func (sftpKit) Manifest() kit.Manifest {
	return kit.Manifest{
		ID:          "edgekit.kit.sftp",
		Name:        "SFTP",
		Version:     "0.1.0",
		License:     "Apache-2.0",
		Runtime:     "builtin",
		Activation:  []string{"onDeviceKind:ssh"},
		Description: "远端文件浏览与上传下载（复用 SSH 连接）",
	}
}

func (k sftpKit) Tools() []kit.Tool {
	return []kit.Tool{
		{
			Name:        "sftp_status",
			Description: "查询 SFTP 文件传输会话是否已连接",
			Risk:        kit.RiskRead,
			Schema:      obj(nil),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.s == nil || !k.s.IsConnected() {
					return "SFTP 未连接", nil
				}
				return "SFTP 已连接", nil
			},
		},
		{
			Name:        "sftp_list",
			Description: "列出 SFTP 远端目录内容",
			Risk:        kit.RiskRead,
			Schema:      obj(map[string]any{"path": strType()}, "path"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.s == nil || !k.s.IsConnected() {
					return "", fmt.Errorf("SFTP 未连接")
				}
				entries, err := k.s.List(argString(args, "path"))
				if err != nil {
					return "", err
				}
				var b strings.Builder
				for _, e := range entries {
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
			Name:        "sftp_download",
			Description: "把远端文件下载到本地工作区，返回本地路径",
			Risk:        kit.RiskRead,
			Schema:      obj(map[string]any{"path": strType()}, "path"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.s == nil || !k.s.IsConnected() {
					return "", fmt.Errorf("SFTP 未就绪（请先连接 SSH）")
				}
				remote := argString(args, "path")
				data, err := k.s.Download(remote)
				if err != nil {
					return "", err
				}
				local, err := workspace.Write(filepath.Base(remote), data)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("已下载 %s → %s（%d 字节）", remote, local, len(data)), nil
			},
		},
		{
			Name:        "sftp_upload",
			Description: "把文本内容写入 SFTP 远端文件",
			Risk:        kit.RiskMutate,
			Schema:      obj(map[string]any{"path": strType(), "content": strType()}, "path", "content"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.s == nil || !k.s.IsConnected() {
					return "", fmt.Errorf("SFTP 未连接")
				}
				path := argString(args, "path")
				if err := k.s.Upload(path, []byte(argString(args, "content"))); err != nil {
					return "", err
				}
				return "已写入 " + path, nil
			},
		},
	}
}
