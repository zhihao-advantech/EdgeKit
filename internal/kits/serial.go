package kits

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"edgekit/internal/kit"
)

// serialKit exposes the focused serial session.
type serialKit struct{ s kit.Serial }

func (serialKit) Manifest() kit.Manifest {
	return kit.Manifest{
		ID:          "edgekit.kit.serial",
		Name:        "Serial",
		Version:     "0.1.0",
		License:     "Apache-2.0",
		Runtime:     "builtin",
		Activation:  []string{"onStartup", "onDeviceKind:serial"},
		Description: "串口收发、命令执行与设备目录抓取",
	}
}

func (k serialKit) Tools() []kit.Tool {
	return []kit.Tool{
		{
			Name:        "serial_status",
			Description: "查询串口会话是否已打开及其参数",
			Risk:        kit.RiskRead,
			Schema:      obj(nil),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.s == nil || !k.s.IsOpen() {
					return "串口未打开", nil
				}
				return "串口已打开: " + k.s.Port(), nil
			},
		},
		{
			Name:        "serial_read",
			Description: "读取串口最近接收到的数据（文本）",
			Risk:        kit.RiskRead,
			Schema:      obj(nil),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.s == nil || !k.s.IsOpen() {
					return "", fmt.Errorf("串口未打开")
				}
				data := k.s.Recent()
				if len(data) == 0 {
					return "(暂无数据)", nil
				}
				if len(data) > 8192 {
					data = data[len(data)-8192:]
				}
				return strings.ToValidUTF8(string(data), "\uFFFD"), nil
			},
		},
		{
			Name:        "serial_write",
			Description: "向串口发送数据（会自动追加换行）",
			Risk:        kit.RiskMutate,
			Schema:      obj(map[string]any{"data": strType()}, "data"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.s == nil || !k.s.IsOpen() {
					return "", fmt.Errorf("串口未打开")
				}
				data := argString(args, "data")
				if !strings.HasSuffix(data, "\n") {
					data += "\n"
				}
				if err := k.s.Write([]byte(data)); err != nil {
					return "", err
				}
				return "已发送: " + strconv.Quote(strings.TrimRight(data, "\n")), nil
			},
		},
		{
			Name:        "serial_exec",
			Description: "通过串口向设备发送命令并抓取回显（需要设备侧有 shell，串口已打开）",
			Risk:        kit.RiskMutate,
			Schema:      obj(map[string]any{"command": strType()}, "command"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.s == nil || !k.s.IsOpen() {
					return "", fmt.Errorf("串口未打开")
				}
				out, err := k.s.RunCapture(argString(args, "command"), 500*time.Millisecond, 8*time.Second)
				if err != nil {
					return "", err
				}
				return kit.Truncate(strings.TrimSpace(out), 16*1024), nil
			},
		},
	}
}
