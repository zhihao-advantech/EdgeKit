package main

import (
	"context"
	"fmt"
	"os"

	"edgekit/internal/bridge"
	"edgekit/internal/mcp"
	"edgekit/internal/runtime"

	"github.com/gorilla/websocket"
)

// runMCP serves EdgeKit's tools to an MCP client (OpenClaw, Claude Code, ...)
// over stdio, forwarding every call to the running EdgeKit instance so the
// agent sees exactly the devices the user already has connected.
//
// stdout carries the protocol; all logging goes to stderr.
func runMCP() error {
	ep, err := runtime.Read()
	if err != nil {
		return err
	}
	conn, _, err := websocket.DefaultDialer.Dial(ep.WS, nil)
	if err != nil {
		return fmt.Errorf("连接 EdgeKit 失败 (%s): %w", ep.WS, err)
	}
	defer conn.Close()

	b := bridge.New(conn)
	go b.Run()
	return mcp.Serve(context.Background(), os.Stdin, os.Stdout, b, "edgekit", appVersion)
}
