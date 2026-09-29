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
  `CodeEdit` / `Timeline` / `Sessions` / `Test`，共 28 个工具；可在「工具 → Kits 管理」启停，
  依赖设备的 Kit 在未连接设备时自动隐藏。
- **MCP**：`edgekit mcp` 把同一套工具暴露给 OpenClaw / Claude Code 等外部 Agent。
- **连接器 / 技能（右侧并列面板）**：整个界面最右侧的两个同级面板。
  **连接器**：作为 MCP **客户端**连接外部 MCP 服务（知识库、数据库、文件系统等），
  其工具并入同一工具面（名称加连接器前缀），支持添加 / 连接 / 停用 / 重连 / 移除；
  **技能**：列出内置与已安装的 Agent 技能（`~/.agents/skills`），支持安装 / 查看 / 移除。
  可从「连接器」「技能」按钮、工具菜单或「视图 → 显示连接器 / 技能」打开，两者可同时显示。

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

### Windows

- **运行环境**：Windows 10/11，需 **WebView2 Runtime**（随 Edge 一起提供，通常已装；
  否则安装 Evergreen Runtime）。
- **构建**：需要 cgo 与 Windows C/C++ 工具链——`zig`
  （`zig cc -target x86_64-windows-gnu`）或 mingw-w64。脚本会优先使用 `PATH` 中的
  工具，或用 `CC`/`CXX` 指定：

```bash
make windows            # 在 Linux/macOS 上交叉编译出 dist/edgekit.exe
# 或在 Windows 本机（已装工具链）：
packaging/build-windows.sh edgekit.exe
```

生成的 `edgekit.exe` 为无控制台窗口的 GUI 程序；`edgekit.exe mcp` 仍可作为 stdio
MCP 桥接使用。

#### Windows 提示「不安全的/未知发布者」（SmartScreen）

未签名、且带 Internet 标记（Mark of the Web，从网络/邮件下载）的 exe 会触发
SmartScreen。解决办法：

- **签名**：给构建脚本传入代码签名证书：
  ```bash
  WINDOWS_PFX=my.pfx WINDOWS_PFX_PASS=secret \
  WINDOWS_TIMESTAMP_URL=http://timestamp.digicert.com \
    packaging/build-windows.sh
  ```
  OV 证书会逐步积累信誉；EV 证书或链到企业受信任根/发布者的证书可立即信任。
- **企业内网**：用内部 CA 签发证书，并通过 GPO/Intune 把该 CA 部署到每台机器
  （受信任的根 + 受信任的发布者），不再弹窗。
- **单机**：右键文件 → 属性 → 勾选「解除锁定」，或 PowerShell 执行
  `Unblock-File .\edgekit.exe`。通过局域网共享拷贝或本机构建则不携带该标记。

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

#### 局域网内其他设备上的 Agent

内部接口只监听回环且无鉴权，绝不暴露到网络。要让另一台设备上的 Agent 接入本机，
在本机通过 SSH 运行 stdio 桥接即可——把远端 Agent 的 MCP server 配置为：

```bash
ssh -o BatchMode=yes user@本机地址 edgekit mcp
```

MCP 协议走 SSH 通道，EdgeKit 始终只在回环。也可在「工具 → 远程 Agent 接入（SSH）…」
中填写主机 / 端口 / 用户 / 身份文件，界面会生成上面的命令与可直接粘贴的
`mcpServers` JSON 配置，一键复制（配置会被记住）。前提：本机 EdgeKit 正在运行，
远端能用该用户免密 SSH 登录（用密钥；`BatchMode` 不会提示输入密码）。

配置保存在 `~/.config/edgekit/settings.json`（含 API Key，权限 0600）。

### 测试会话

会话列表「测试会话 → ＋」新建，或点标签栏右侧的「测试会话」；右侧按
**连接 → 运行 → 生成 → 归档** 四步执行。每个**检查项**可以填一条 shell 指令，或点「脚本」
从工作区选一个 `tests/*.sh` 执行（SSH 经 SFTP 上传，串口用 heredoc）；都能设期望输出（正则）、
超时与退出码要求。「打开文件夹」可直接打开 `tests/` 目录放入脚本。用例可保存到工作区 `tests/<名称>.test.json`（也可直接编辑
该文件后重新加载），运行结果与 Markdown 报告归档到
`~/EdgeKit/workspace/tests/runs/<id>/`；失败也会照常出报告并归档。目标可选「全部会话」
做**批量运行**；报告会与同名上次运行对比，标出**回归 / 修复**。Agent 也可用
`test_list` / `test_run` / `test_report` 工具驱动测试。

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
internal/{serial,sshclient,sftpx,workspace,timeline,policy,mcp,mcpclient,acp,netdiag}/
packaging/              .deb / .run 打包脚本与依赖检查
third_party/webview_go/ WebKit 4.0/4.1 build tag 的 webview fork
```

## 安全说明

- SSH 主机密钥不做校验，仅打印指纹；请勿用于不可信网络。
- 内部 WebSocket 仅监听回环地址，不对局域网开放（`-addr` 也拒绝非回环地址）。
- 修改性工具默认逐次确认；「自动执行」请谨慎开启。
- `dangerous` 级别工具**始终**需要手动确认；**外部连接器的工具不随「自动执行」放行**，
  一律逐次审批，且工具名统一加连接器前缀避免与内置工具混淆。

### 外部 MCP 连接器

在右侧 **连接器**面板中点「添加连接器…」即可声明并连接
（写入 `~/.config/edgekit/connectors.json`）；也可直接编辑该文件，重启或刷新面板即可挂载：

```json
{
  "servers": [
    { "id": "kb", "name": "知识库", "command": "kb-mcp",
      "args": ["--index", "/data/kb"], "enabled": true, "risk": "read" }
  ]
}
```

`risk` 缺省为 `mutate`（需审批）；外部工具名为 `kb_<远端工具名>`，随连接/断开动态出现与消失。

### Agent 技能

与「连接器」并列的 **技能**面板，列出 EdgeKit 内置技能（仓库 `skills/` 目录，
或环境变量 `EDGEKIT_SKILLS_DIR`）与已安装到 `~/.agents/skills` 的技能。
「安装」将内置技能链接到 Agent 技能目录，「查看」显示其 `SKILL.md`，「移除」删除已安装副本。

## 许可证

Apache License 2.0，见 [LICENSE](LICENSE)。
