package kits

import (
	"context"
	"fmt"
	"strings"

	"edgekit/internal/kit"
)

// sessionsKit exposes the device-session directory: a brain lists the open
// sessions so it can address one explicitly with the shared `session`
// argument instead of relying on whichever session happens to be focused.
type sessionsKit struct{ list func() []kit.SessionInfo }

func (sessionsKit) Manifest() kit.Manifest {
	return kit.Manifest{
		ID:          "edgekit.kit.sessions",
		Name:        "Sessions",
		Version:     "0.1.0",
		License:     "Apache-2.0",
		Runtime:     "builtin",
		Activation:  []string{"onStartup"},
		Description: "设备会话目录：查看已连接的串口 / SSH 会话，供多板寻址",
	}
}

func (k sessionsKit) Tools() []kit.Tool {
	return []kit.Tool{
		{
			Name: "sessions_list",
			Description: "列出当前已连接的设备会话（id / 类型 / 状态 / 标签）。" +
				"多板同时连接时，用其中的 id 作为其它设备工具的 session 参数指定目标板；" +
				"不传 session 时作用于当前聚焦会话。",
			Risk:   kit.RiskRead,
			Schema: obj(nil),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				var list []kit.SessionInfo
				if k.list != nil {
					list = k.list()
				}
				if len(list) == 0 {
					return "(没有设备会话；请在界面新建串口或 SSH 会话)", nil
				}
				var b strings.Builder
				for _, si := range list {
					state := "未连接"
					if si.Connected {
						state = "已连接"
					}
					fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", si.ID, si.Kind, state, si.Label)
				}
				return b.String(), nil
			},
		},
	}
}
