# EdgeKit

**AI 加速端侧设备升级。**

以 **Agent 会话**为中枢，把端侧设备调试与升级所需的手段整合到一个原生桌面应用中：
AI Agent、串口调试、SSH 终端、工作区文件管理，支持**同时连接多块板**。

界面使用 HTML/CSS/JS 编写，**内嵌在可执行文件中**，运行程序即由 **WebKit2GTK**
（`libwebkit2gtk-4.0`）在原生窗口里直接打开，无需浏览器、无需手动访问任何地址。
后端由 Go 提供各项能力，二者通过本机回环地址上的 JSON / WebSocket 协议通信。

```
┌──────────────────────────────────────┐        ┌────────────────────────────────┐
│  WebView (WebKit2GTK)                │  ws:// │  Go backend                    │
│  菜单栏 / 会话面板 / 标签页 / Agent  │ <────> │  internal/server               │
└──────────────────────────────────────┘        │   ├── agent     Agent 中枢     │
                                                │   ├── serial    串口           │
                                                │   ├── sshclient SSH 终端       │
                                                │   ├── sftpx     远端文件       │
                                                │   ├── workspace 本地工作区     │
                                                │   └── netdiag   网络检查       │
                                                └────────────────────────────────┘
```

## Kit 架构

能力以 **Kit**（能力包）形式提供，宿主（Host）聚合各 Kit 贡献的工具，**内置 Agent、UI 协议与后续的 MCP Server 共用同一份定义**：

- 内置 Kit：`Host`、`Network`、`Serial`、`SSH`、`SFTP`、`Workspace`，共 18 个工具
- 每个 Kit 带 `Manifest`（`id` / `name` / `version` / `license` / `runtime` / `activation`）
  与一组 `Tool`（JSON Schema + 风险等级 `read` / `mutate` / `dangerous`）
- 宿主对非 `read` 工具要求审批（沿用现有 approval 流程）
- 「帮助 → 关于」可查看已加载的 Kit 与工具统计

代码结构：

```
internal/kit/    Kit / Manifest / Tool / Registry 与能力接口（Serial / SSH / SFTP）
internal/kits/   内置 Kit 实现，Builtin(deps) 返回全部
internal/agent/  只消费 kit.Registry，不再硬编码工具
```

后续阶段：`kit.json` 外置清单、外置 Kit（协议走 MCP）、Kits 管理页。

## 设备模型与时间线

每一次观测/动作都会进入设备的**时间线**（append-only，带序号与时间戳）。它位于 provider 与消费者之间，
是 UI、内置 Agent 与（后续）MCP 共用的**单一事实源**：

- 位置：`internal/timeline/`
- API：`Append` / `Since(seq, limit)` / `Wait(ctx, filter, timeout)` / `LastSeq`
- 宿主为每个设备会话维护一条时间线（上限 5000 条），provider 事件在广播前先落库
- 用途：**UI 回放**（刷新后恢复滚动内容）、跨通道关联、后续的 `wait_for_output` 与审计
- 协议：`timeline` 请求 → `timeline.records` 事件（字段 `seq/time/channel/kind/data`）

## MCP（OpenClaw / Claude Code 接入）

EdgeKit 可以作为一个 **MCP 工具服务**被外部 Agent 驱动：`edgekit mcp` 用 stdio 讲 MCP，
并把每个调用转发给**正在运行的 App**，因此 Agent 用的是你已经连好的串口 / SSH 会话，
界面里的审批也照常生效。

```bash
# 1) 先启动 App（它会把端点发布到 ~/.config/edgekit/runtime.json）
./build/edgekit

# 2) 加入 OpenClaw（add 会先连上探活、成功后才保存）
openclaw mcp add edgekit --command "$PWD/build/edgekit" --arg mcp
openclaw mcp probe edgekit        # -> edgekit: 18 tools
```

- 协议：MCP（JSON-RPC 2.0 over stdio），实现 `initialize` / `tools/list` / `tools/call`
- 工具来自 kit Registry：**只读工具直接执行；修改性工具由 `internal/policy` 策略门
  在 App 界面弹出审批**，与内置 Agent 走同一道闸
- 其它 MCP 客户端（Claude Code / Codex CLI / Goose…）同样用 stdio 命令 `edgekit mcp`
- App 未运行时桥接会明确报错——它复用 App 的会话，不会自己去占用串口

## Agent 连板调试

如果要让 Agent（而非人工）用 EdgeKit 做串口 / SSH 连板调试，见
[`skills/edgekit-board-debug/SKILL.md`](skills/edgekit-board-debug/SKILL.md)：
包含构建启动、能力清单、内置 Agent 工具、WebSocket 协议速查与典型调试流程。

安装为 Agent skill：

```bash
ln -s "$PWD/skills/edgekit-board-debug" ~/.agents/skills/edgekit-board-debug
```

## 界面

整体布局参考 **MobaXterm**：一级菜单栏 + 左侧「会话 / 工作区 / 会话设置」+ 顶部会话标签页 + 黑色终端。

- **一级菜单栏**：`会话`、`终端`、`工具`、`视图`、`帮助`，点击展开二级下拉菜单；
  鼠标移到相邻菜单会自动切换，`Esc` 或点击空白处收起。
  - `会话`：新建 Agent / 串口 / SSH 会话，关闭当前 / 全部会话
  - `终端`：自动滚动、时间戳、HEX 显示、本地回显（可勾选项），清空、复制、粘贴
  - `工具`：刷新串口列表、Agent 设备巡检 / 查看日志
  - `视图`：显示会话面板 / 工作区、终端字号调整
  - `帮助`：关于 EdgeKit
- **左侧「会话」面板**：Agent 会话 + 若干**设备会话**（串口 / SSH，可同时连接多块板，
  点右上「＋」新建），带状态指示灯，点击切换。
- **顶部标签**：每个设备会话一个标签，可关闭。
- **「工作区」面板**（会话与设置之间）：随当前会话切换语义——
  Agent 跟随执行目标（Local → 本地工作区，Remote → 远端 SFTP），
  串口显示实验性的设备目录（`ls -la` 抓取），SSH 显示「本地 / 远端」两侧；
  只要 SSH 已连接，远端在任意会话都可用，可上传下载。
- **「会话设置」**：随当前会话切换。
- **会话标签页**：每个会话一个可关闭标签，关闭标签即断开对应连接。
- **终端区**：黑色终端，底部状态栏显示连接状态与收发字节数。

## 功能

### 串口调试
- 自动枚举串口设备（`/dev/ttyUSB*`、`/dev/ttyACM*`、`/dev/ttyS*`），显示 VID/PID/序列号
- 可配置波特率、数据位、校验位、停止位
- 实时收发日志，支持时间戳、自动滚动、HEX 显示
- 文本显示自动过滤 ANSI 转义序列（跨分片），接收 / 发送字节数统计

### SSH 终端
- 密码或私钥（PEM / OpenSSH）认证，支持私钥口令
- 连接成功后自动打开交互式 Shell（申请 PTY，支持窗口尺寸变更）
- 显示远端主机密钥指纹

### Agent 会话（AI 中枢）

用自然语言驱动串口 / SSH / 工作区完成调试与升级任务，是 EdgeKit 的核心入口。

- **执行目标 Local / Remote**：在左侧「会话信息」面板切换指令作用在哪一侧（默认 **Remote** 远端设备）；
  Remote 走 SSH（未连则串口），Local 走本机 `local_exec`。Normal 模式的磁盘/内存/日志/巡检按目标分流，
  AI 模式会把目标写进 system prompt 并允许用户显式覆盖。
- **两种模式**：
  - **AI 模式**：配置 OpenAI 兼容的 Base URL / API Key / 模型后，走 function-calling 循环，
    模型按需调用工具并汇总结果；
  - **Normal 模式**：未配置模型时，用内置流程完成巡检、日志、磁盘、内存、进程、系统版本、ping 等。
- **工具集**：`local_info` / `local_exec`（本机）、`net_ping` / `net_check_port` / `net_resolve`、
  `serial_status` / `serial_read` / `serial_write` / `serial_exec`、
  `ssh_status` / `ssh_exec`、`sftp_status` / `sftp_list` / `sftp_download` / `sftp_upload`、
  `workspace_list` / `workspace_read` / `workspace_write`。
- **安全确认**：只读工具自动执行；写串口、执行命令、上传文件等**修改性操作会先弹出确认**，
  用户点「允许执行」后才会运行（可勾选「自动执行修改性操作」跳过确认）。
- **上下文感知**：Agent 知道当前串口 / SSH 是否已连接及目标，直接复用已建立的会话，下载落到本地工作区。
- **快捷指令**：设备巡检 / 系统日志 / 磁盘 / 内存 / 系统版本。
- 会话记录、工具调用与结果都会以对话卡片形式保留在面板中。

> 网络诊断（ping / 端口 / DNS）不再是独立会话，已并入 Agent 工具，也可直接在 SSH 终端里执行。

### 工作区（文件面板）

不再是独立会话，而是常驻左侧、随当前会话切换语义的文件面板：

- **本地**：`~/EdgeKit/workspace` 沙箱目录（路径越界会被限制在根内），Agent 也以此为准；
- **远端**：SSH 连接后自动建立 SFTP（复用同一条 SSH 连接，无需单独凭据），
  支持进入子目录 / 返回上级；
- **串口**：向设备发送 `ls -la` 抓取目录（实验性，依赖设备侧 shell）；
- 工具条：上传、下载、刷新、新建文件夹、新建文件、删除、编辑（文本文件在线编辑）；
  单栏列表显示名称与大小，状态区显示条目数 / 选中项；
- 单文件传输上限 16 MiB。

> 串口是裸字节流，本身没有文件协议，因此「串口」侧仅做 `ls` 抓取，不提供文件传输。

### 终端输入（PuTTY / MobaXterm 风格）

终端本身就是收 / 发缓冲区，**点击终端后直接键盘输入**，没有单独的发送栏：

- 可打印字符、`Enter`（`\r`）、`Backspace`（`0x7F`）、`Tab`、`Esc` 直接下发；
- 方向键 / Home / End / Delete / PgUp / PgDn 发送对应 ANSI 序列，远端历史与行编辑可用；
- `Ctrl+A`~`Ctrl+Z` 发送控制字符（如 `Ctrl+C` 中断），`Alt+<键>` 发送 `ESC` 前缀；
- `Ctrl+Shift+C` 复制、`Ctrl+Shift+V` 粘贴，也支持系统粘贴事件；
- 终端末尾显示块状光标，未聚焦时为空心框；
- 串口设备通常会回显，若设备不回显可勾选「终端 → 本地回显」。

显示端采用**面向行的终端模型**（见下），而不是逐块删除转义字符：

- 远端回显的退格（`\b` / `0x7F`）、回车重绘（`\r`）、行内擦除（CSI `K`）、
  光标左右移动（CSI `C`/`D`/`G`）都会被正确解析，命令行编辑不再出现乱码；
- 跨串口分片到达的转义序列由持久状态机缓存拼接，不受分片边界影响；
- 屏幕擦除（CSI `J`）与颜色（CSI `m`）暂不处理，以保留滚动日志。

> 说明：该模型覆盖 shell 行编辑、串口日志等常见场景；若要完整支持 `vim`、
> `top` 这类全屏程序，需要引入 xterm.js 之类的完整终端模拟器。

## 性能设计

高吞吐场景（高速串口、SSH 刷屏日志）下做了两层优化：

**后端：数据合批**
- 串口 / SSH 的数据事件按 `(通道, 方向)` 聚合，每 **12ms** 或累计 **32KB** 触发一次下发，
  把大量碎片小包合并成一条 WebSocket 消息；
- 日志 / 生命周期事件（已打开、错误等）不做合批，且会**先冲刷同通道的待发数据**，
  保证顺序不乱；
- 效果：115200 波特率、按字节投递（约 11520 次/秒）时，下发消息数从
  **11520/s 降到约 83/s**（`go test ./internal/server -bench Batcher` 可复现）。

**前端：行缓冲 + 帧内合并**
- 当前行用**增量字符串**维护：追加是摊还 O(1)，只有行内覆盖（退格/回车重绘）才做切片，
  避免逐字符 `join()` 造成的 O(n²)；
- 活动行的 DOM 写入与滚动用 `requestAnimationFrame` **合并到每帧一次**，
  无论一帧内到达多少字节都只更新一次 DOM。

实测（Node 纯字符串处理，见下表）：

| 场景 | 逐块字符串拼接 | 逐字符 join+slice | 当前增量模型 |
| --- | --- | --- | --- |
| 20KB 单行，1 字节/块 | 5.6 ms | 1011 ms | **0.9 ms** |
| 1MB，64 字符/行 | 0.7 ms | 8109 ms | **0.7 ms** |

> 合批会引入最多约 12ms 的显示延迟，对串口监视与 SSH 交互无感知影响。

## 环境要求

- Go 1.22+
- GTK 3 与 WebKit2GTK 4.0 开发库：

```bash
sudo apt update
sudo apt install -y build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.0-dev
```

串口访问权限（把当前用户加入 `dialout` 组后重新登录）：

```bash
sudo usermod -aG dialout "$USER"
```

## 构建与运行

```bash
make build          # 生成 build/edgekit
./build/edgekit     # 直接打开界面窗口

make run            # go run
```

命令行参数：

| 参数 | 说明 |
| --- | --- |
| `-addr` | 内部 WebSocket 监听地址，默认 `127.0.0.1:0`（随机端口，仅供界面通信） |
| `-debug` | 打开 WebView 开发者工具 |

运行后会直接弹出应用窗口，界面资源全部内嵌，不依赖浏览器或外部文件。
需要在图形环境下运行（X11 / Wayland）。

## 关于链接错误 `GLIBCXX_3.4.30`

部分发行版的 `libwebkit2gtk-4.0` 使用较新的 GCC 构建，而默认 `g++` 搜索路径中的
`libstdc++` 较旧，链接时会报：

```
undefined reference to `std::condition_variable::wait(...)@GLIBCXX_3.4.30'
```

`Makefile` 已内置处理：在 `build/.libstdcxx/` 下创建指向运行时 `libstdc++.so.6` 的
软链接，并通过 `CGO_LDFLAGS=-L...` 让链接器优先使用它，无需修改系统文件。
若使用 `go build` 而非 `make`，请自行传入相同的 `CGO_LDFLAGS`。

## 目录结构

```
cmd/edgekit/            程序入口，创建 WebView 并加载本地服务
internal/serial/        串口管理器（枚举 / 打开 / 读写）
internal/sshclient/     SSH 终端（连接 / exec / 交互式 Shell）
internal/sshutil/       SSH 连接参数与拨号（sshclient 与 sftpx 共用）
internal/timeline/      设备时间线（append-only 记录，UI/Agent/MCP 共用）
internal/policy/        审批策略门（read / mutate / dangerous）
internal/mcp/           最小 MCP server（JSON-RPC 2.0 over stdio）
internal/runtime/       运行端点发布（供 edgekit mcp 发现 App）
internal/kit/           Kit 扩展模型（Manifest / Tool / Registry / 能力接口）
internal/kits/          内置 Kit（Host / Network / Serial / SSH / SFTP / Workspace）
internal/agent/         AI Agent（消费 Registry、Normal 模式、模型调用、审批）
internal/netdiag/       网络检查（ping / 端口 / DNS / 本机信息）
internal/sftpx/         SFTP 文件浏览与传输（挂在 SSH 连接上）
internal/workspace/     本地工作区（沙箱化文件操作）
internal/server/        HTTP + WebSocket 服务，连接前后端
internal/server/web/    内嵌的前端资源（HTML/CSS/JS）
```

## WebSocket 协议

请求（浏览器 → 后端）：

所有设备相关请求都带 `sessionId`（连接时返回，多路会话用它区分）。

| 分组 | type | payload |
| --- | --- | --- |
| 会话 | `session.focus` / `session.close` | `sessionId` |
| 串口 | `serial.list` | – |
| | `serial.open` | `{port, baud, dataBits, parity, stopBits}` → 新建会话 |
| | `serial.close` | `sessionId` |
| | `serial.write` | `{data, hex}` + `sessionId` |
| SSH | `ssh.connect` | `{host, port, user, password, privateKey, passphrase}` → 新建会话 |
| | `ssh.disconnect` | `sessionId` |
| | `ssh.exec` | `{command}` |
| | `ssh.shell.start` | `{cols, rows}` |
| | `ssh.shell.write` | `{data}` |
| | `ssh.shell.resize` | `{cols, rows}` |
| | `ssh.shell.close` | – |
| Agent | `agent.send` | `{text}` |
| | `agent.config` | `{baseUrl, apiKey, model, autoRun}` |
| | `agent.approve` | `{id, allow}` |
| | `agent.cancel` / `agent.reset` | – |
| 设置 | `settings.set` | 任意键值（持久化到本机配置文件） |
| 工作区 | `fs.list` | `{side: local\|remote\|serial, path}` |
| | `fs.mkdir` / `fs.newfile` | `{side, path}` |
| | `fs.upload` | `{dir, name, data}`（本地 → 远端） |
| | `fs.download` | `{path}`（远端 → 本地工作区） |

事件（后端 → 浏览器）：`sessions`（连接快照）、`session.opened` / `session.closed` /
`session.focus`、`serial.ports`、`serial.status`、`serial.event`、
`ssh.status`、`ssh.event`、`fs.files`、`fs.done`、
`agent.event`、`agent.config`、`settings`、`error`。
其中 `serial.status` / `serial.event` / `ssh.status` / `ssh.event` 都带 `sessionId`。
其中二进制数据统一使用 base64 编码。

## 安全说明

- SSH 主机密钥不做校验，仅打印指纹，便于调试内网设备；请勿用于不可信网络。
- 内部 WebSocket 仅监听回环地址，供本机 WebView 使用，不对局域网开放。
- Agent 的修改性工具默认需要用户逐次确认；「自动执行」会跳过确认，请谨慎开启。
- 界面设置（含 API Key）保存在 `~/.config/edgekit/settings.json`（权限 0600）。

## 许可证

EdgeKit 以 **Apache License 2.0** 开源，全文见 [LICENSE](LICENSE)。

```
Copyright 2026 EdgeKit contributors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
```

每个 Kit 自带 `license` 字段；外置 Kit 将以独立进程（MCP）接入，各自独立授权。

## 路线图

EdgeKit 定位为 AI 加速端侧设备升级。当前版本先完成通用调试手段，
后续将逐步加入：AI 辅助故障诊断与日志分析、设备自动发现、批量运维与智能编排。
