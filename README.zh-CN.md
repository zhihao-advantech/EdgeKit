# EdgeKit

[English](README.md) · 中文

**AI 加速端侧设备升级。**

以 **Agent 会话**为中枢，把端侧设备调试与升级所需的手段整合进一个原生桌面应用：
AI Agent、串口调试、SSH 终端、SFTP 工作区文件管理，支持**同时连接多块板**。

界面用 HTML/CSS/JS 编写并**内嵌在可执行文件中**，运行后由 WebKit2GTK 在原生窗口直接打开，
无需浏览器；后端为 Go，二者通过本机回环地址上的 JSON / WebSocket 通信。

## 功能简介

- **Agent 会话**：自然语言驱动串口 / SSH / 工作区完成调试与升级。大脑可选内置
  （OpenAI 兼容 function-calling；未配置模型时走 Normal 内置流程）或外置 ACP
  （Hermes / OpenClaw）。修改性操作默认弹确认。
- **串口**：自动枚举设备，可配波特率 / 数据位 / 校验 / 停止位，实时收发（时间戳、HEX、字节统计）。
- **SSH**：密码或私钥认证，交互式 Shell（PTY、窗口尺寸变更），显示主机密钥指纹。
- **工作区**：本地沙箱 `~/EdgeKit/workspace` 与远端 SFTP，上传 / 下载 / 在线编辑（单文件 ≤ 16 MiB）。
- **多板**：可同时连接多块板；`sessions_list` 列出会话，设备工具用 `session` 参数指定目标板。
- **能力包（Kit）**：`Host` / `Network` / `Serial` / `SSH` / `SFTP` / `Workspace` /
  `CodeEdit` / `Timeline` / `Sessions`，共 25 个工具；可在「工具 → Kits 管理」启停，
  依赖设备的 Kit 在未连接设备时自动隐藏。
- **MCP**：`edgekit mcp` 把同一套工具暴露给 OpenClaw / Claude Code 等外部 Agent。

## 环境要求

- Go 1.22+
- GTK 3 与 WebKit2GTK 开发库（Ubuntu 22.04 / Debian 12 用 4.0；Ubuntu 24.04+ / Debian 13+ 用 4.1）：

```bash
# Ubuntu 22.04 / Debian 12
sudo apt install -y build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.0-dev
# Ubuntu 24.04+ / Debian 13+
sudo apt install -y build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev
```

串口权限（加入 `dialout` 组后重新登录）：

```bash
sudo usermod -aG dialout "$USER"
```

## 构建与运行

```bash
make build          # 生成 build/edgekit
./build/edgekit     # 打开界面窗口
make run            # go run
make headless       # 只运行后端，不开窗口（服务器 / 远程）
```

WebKit 版本由 `pkg-config` 自动探测，也可显式指定：

```bash
make WEBKIT=4.1 build   # 强制 4.1 变体
make info               # 查看将构建的版本与 WebKit 变体
```

| 参数 | 说明 |
| --- | --- |
| `-addr` | 内部 WebSocket 监听地址，默认 `127.0.0.1:0`（仅供界面通信） |
| `-debug` | 打开 WebView 开发者工具 |
| `-headless` | 只运行后端，不打开窗口 |

> 链接若报 `GLIBCXX_3.4.30`，`make` 已内置规避；改用 `go build` 时请传入相同的 `CGO_LDFLAGS`。

## 打包与安装

两种包都会**校验运行时依赖版本**：

```bash
make package-deb    # dist/edgekit_<版本>_<架构>.deb
make package-run    # dist/EdgeKit-<版本>-webkit<4.0|4.1>-<架构>.run
make package        # 两种都生成
```

```bash
sudo apt install ./dist/edgekit_<版本>_<架构>.deb   # apt 自动解析依赖

./dist/EdgeKit-<版本>-webkit<4.0|4.1>-<架构>.run    # root → /usr/local，普通用户 → ~/.local
./EdgeKit-*.run --check                             # 只检查依赖
./EdgeKit-*.run --uninstall                         # 卸载
```

> WebKitGTK 4.0 与 4.1 的 ABI 不兼容，需按系统分别打包：
> 22.04 / Debian 12 用 `WEBKIT=4.0`，24.04+ / Debian 13+ 用 `WEBKIT=4.1`。

## 使用

1. 启动 `edgekit`：顶部菜单栏、左侧「会话 / 工作区 / 会话设置」、顶部标签页、黑色终端；
   右上角**齿轮图标**是终端显示设置（自动滚动 / 时间戳 / HEX / 本地回显），修改后自动保存并全局生效。
2. 新建会话：「会话 → 新建串口 / SSH 会话」，可同时连接多块板并切换标签。
3. 使用 Agent：「会话设置 → Agent」填入 OpenAI 兼容的 Base URL / API Key / 模型，
   或把 Agent 大脑切换为 Hermes / OpenClaw；修改性操作会在界面弹确认。
4. 工作区：左侧面板上传 / 下载 / 编辑文件，SSH 连接后即可访问远端。
5. 接入外部 Agent（需先启动 App）：

```bash
openclaw mcp add edgekit --command edgekit --arg mcp
openclaw mcp probe edgekit        # 列出可用工具（随设备连接变化）
```

配置保存在 `~/.config/edgekit/settings.json`（含 API Key，权限 0600）。

### 让 Agent 自己连板调试

见 [`skills/edgekit-board-debug/SKILL.md`](skills/edgekit-board-debug/SKILL.md)；安装为 Agent skill：

```bash
ln -s "$PWD/skills/edgekit-board-debug" ~/.agents/skills/edgekit-board-debug
```

## 目录结构

```
cmd/edgekit/            程序入口（WebView + 服务启动）
internal/server/        HTTP + WebSocket 服务与内嵌前端资源
internal/kit/ kits/     Kit 模型与内置能力包
internal/agent/         AI Agent（内置 / ACP）
internal/{serial,sshclient,sftpx,workspace,timeline,policy,mcp,acp,netdiag}/
packaging/              .deb / .run 打包脚本与依赖检查
third_party/webview_go/ WebKit 4.0/4.1 build tag 的 webview fork
```

## 安全说明

- SSH 主机密钥不做校验，仅打印指纹；请勿用于不可信网络。
- 内部 WebSocket 仅监听回环地址，不对局域网开放。
- 修改性工具默认逐次确认；「自动执行」请谨慎开启。

## 许可证

Apache License 2.0，见 [LICENSE](LICENSE)。
