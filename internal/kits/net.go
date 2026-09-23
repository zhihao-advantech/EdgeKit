package kits

import (
	"context"
	"fmt"
	"strings"

	"edgekit/internal/kit"
	"edgekit/internal/netdiag"
)

// netKit exposes connectivity checks, always run from the local machine.
type netKit struct{}

func (netKit) Manifest() kit.Manifest {
	return kit.Manifest{
		ID:          "edgekit.kit.net",
		Name:        "Network",
		Version:     "0.1.0",
		License:     "Apache-2.0",
		Runtime:     "builtin",
		Activation:  []string{"onStartup"},
		Description: "从本机发起 Ping / TCP 端口 / DNS 检查",
	}
}

func (netKit) Tools() []kit.Tool {
	return []kit.Tool{
		{
			Name:        "net_ping",
			Description: "对目标主机执行 ICMP ping，返回连通性与延迟",
			Risk:        kit.RiskRead,
			Schema:      obj(map[string]any{"host": strType(), "count": intType()}, "host"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				return netdiag.Ping(ctx, argString(args, "host"), argInt(args, "count", 4))
			},
		},
		{
			Name:        "net_check_port",
			Description: "检查目标主机某个 TCP 端口是否开放",
			Risk:        kit.RiskRead,
			Schema:      obj(map[string]any{"host": strType(), "port": intType()}, "host", "port"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				host := argString(args, "host")
				port := argInt(args, "port", 22)
				open, rtt, err := netdiag.CheckPort(host, port)
				if err != nil {
					return "", err
				}
				if open {
					return fmt.Sprintf("%s:%d 开放，耗时 %d ms", host, port, rtt.Milliseconds()), nil
				}
				return fmt.Sprintf("%s:%d 不可达", host, port), nil
			},
		},
		{
			Name:        "net_resolve",
			Description: "解析域名对应的 IP 地址",
			Risk:        kit.RiskRead,
			Schema:      obj(map[string]any{"host": strType()}, "host"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				ips, err := netdiag.Resolve(argString(args, "host"))
				if err != nil {
					return "", err
				}
				return strings.Join(ips, "\n"), nil
			},
		},
	}
}
