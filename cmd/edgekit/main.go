// Command edgekit is a serial port and SSH debugging console for Linux.
//
// Besides the GUI it provides `edgekit mcp`, a stdio MCP bridge that forwards
// tool calls to the running instance, so an MCP client (OpenClaw, Claude Code)
// can drive the devices the user already has connected.
//
// Running the binary opens a native WebKit2GTK window whose UI is embedded in
// the executable and loaded directly into the WebView. A loopback WebSocket
// endpoint (never exposed to a browser) carries live data between the UI and
// the serial / SSH backends.
package main

import (
	"flag"
	"log"
	"os"
	"runtime"

	"edgekit/internal/server"

	webview "github.com/webview/webview_go"
)

// appVersion is reported to MCP clients.
const appVersion = "0.1.0"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := runMCP(); err != nil {
			log.Fatalf("edgekit mcp: %v", err)
		}
		return
	}

	addr := flag.String("addr", "127.0.0.1:0", "内部 WebSocket 监听地址（仅供界面通信）")
	debug := flag.Bool("debug", false, "启用 WebView 开发者工具")
	flag.Parse()

	srv := server.New()
	wsURL, err := srv.Start(*addr)
	if err != nil {
		log.Fatalf("启动内部服务失败: %v", err)
	}
	defer srv.Close()

	// Printed so an external agent can drive the platform over the same
	// loopback WebSocket API the UI uses.
	log.Printf("EdgeKit 内部服务: %s", wsURL)
	if exe, err := os.Executable(); err == nil {
		log.Printf("MCP 接入: openclaw mcp add edgekit --command %s --arg mcp", exe)
	}

	page, err := server.Page(wsURL)
	if err != nil {
		log.Fatalf("加载界面资源失败: %v", err)
	}

	runtime.LockOSThread()

	w := webview.New(*debug)
	if w == nil {
		log.Fatal("创建窗口失败：请确认已安装 libwebkit2gtk-4.0-37 与 libgtk-3-0，并在图形环境下运行")
	}
	defer w.Destroy()

	w.SetTitle("EdgeKit")
	w.SetSize(1280, 820, webview.HintNone)
	w.SetSize(1000, 640, webview.HintMin)
	w.SetHtml(page)
	w.Run()
}
