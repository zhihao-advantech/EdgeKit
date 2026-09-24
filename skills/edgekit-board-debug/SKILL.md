---
name: edgekit-board-debug
description: 使用本机 EdgeKit 平台对端侧设备（开发板 / 嵌入式 Linux / MCU）做串口与 SSH 连板调试。USE FOR: 抓串口启动日志、串口发送命令、SSH 登录板子、在板子上执行命令、列目录/传文件、上传固件、驱动 EdgeKit 内置 Agent。DO NOT USE FOR: 开发 EdgeKit 自身源码。
---

# EdgeKit 连板调试指南（Agent 版）

EdgeKit 是本机的一个桌面调试平台，把**串口**、**SSH**、**工作区文件**和**AI Agent**整合在一起。
本文件面向「会调用本机工具的 Agent」，说明如何启动 EdgeKit 并用它调试端侧设备。

- 平台源码：`EdgeKitUI/`
- 二进制：`EdgeKitUI/build/edgekit`
- 内部接口：本机回环 WebSocket（仅 127.0.0.1，无鉴权）

---

## 1. 构建与启动

```bash
cd EdgeKitUI
make build                      # 生成 build/edgekit（首次会自动处理 libstdc++ 链接问题）
./build/edgekit                 # 打开原生窗口；内部端口随机
./build/edgekit -addr 127.0.0.1:8799   # 固定内部端口，便于外部 Agent 接管
./build/edgekit -debug          # 打开 WebView 开发者工具
```

启动后 stdout 会打印内部服务地址，**外部 Agent 用它来接管**：

```
EdgeKit 内部服务: ws://127.0.0.1:8799/ws
```

> 需要有图形环境（X11 / Wayland）才能启动窗口；无显示时可配合 `xvfb-run -a` 启动后仅用 WebSocket API。

依赖（Ubuntu）：

```bash
sudo apt install -y build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.0-dev
sudo usermod -aG dialout "$USER"   # 串口权限，需重新登录
```

---

## 2. 三种使用方式

| 方式 | 适合 | 入口 |
| --- | --- | --- |
| 图形界面 | 人在现场调试 | 直接操作窗口 |
| 内置 Agent 会话 | 自然语言下指令 | 界面里的「Agent 会话」 |
| **WebSocket API** | **Agent 自动化** | `ws://127.0.0.1:<port>/ws` |

> **多设备**：串口 / SSH 会话可同时开多个（1~5 块板）。每个连接会返回一个
> `sessionId`，后续 `serial.write` / `ssh.exec` / `fs.*` 都要带它；
> `session.focus` 指定 Agent 与工作区当前作用的设备。
>
> 界面里**关闭标签页只是隐藏视图，不断开连接**；真正断开请在左侧「会话信息」
> 面板点「关闭串口 / 断开 SSH」（协议对应 `serial.close` / `ssh.disconnect`）。

图形界面：左侧「会话」→ Agent / 串口 / SSH；「工作区」文件面板；「会话设置」配参数。

内置 Agent：能用自然语言驱动上面全部能力，并在**修改性操作前请求确认**。

WebSocket API：与界面走同一套协议，适合 Agent 直接调用（见第 7 节）。

---

## 3. 串口调试

**连接**：会话 → 新建串口会话 → 选设备 / 波特率 / 数据位 / 校验 / 停止位 → 打开串口。

要点：

- 设备枚举含 VID/PID/序列号；顶部「工具 → 刷新串口列表」可重扫。
- 终端即缓冲区：**点击终端后直接键盘输入**。`Enter`→`\r`、`Backspace`→`0x7F`、
  方向键/Home/End/Delete → ANSI 序列、`Ctrl+A~Z` → 控制字符、`Ctrl+Shift+C/V` 复制粘贴。
- 设备通常回显；若设备不回显，勾选「终端 → 本地回显」。
- 可切换时间戳 / 自动滚动 / HEX 显示；显示端已处理退格、回车重绘、行内擦除。
- 打开串口会清空接收历史缓冲。

**自动化首选**：抓启动日志、发 AT/命令、看崩溃回溯。

---

## 4. SSH 调试

**连接**：会话 → 新建 SSH 会话 → 主机 / 端口 / 用户 / 认证（密码或私钥 PEM）→ 连接。

要点：

- 连接成功后**自动打开交互式 Shell**，可直接输入。
- 主机密钥不校验（内网调试），只打印 SHA256 指纹。
- 连接成功后**自动在同一个连接上建立 SFTP**，供「工作区 → 远端」使用，无需再配一套凭据。
- 支持窗口尺寸变化（`ssh.shell.resize`）。

---

## 5. 工作区（文件面板）

左侧常驻，随当前会话切换语义：

| 当前会话 | 工作区内容 |
| --- | --- |
| Agent | **跟随执行目标**：Local → 本地工作区；Remote → 远端（SFTP）。SSH 未连时只显示本地 |
| 串口 | 设备目录（向串口发 `ls -la` 抓取，**实验性**，依赖设备有 shell）；串口单独调试时隐藏本地。抓取为**静默**执行，不会污染右侧终端 |
| SSH | 「本地 / 远端」两侧切换；远端即 SFTP，可上传下载 |

> 只要 SSH 已连接，「远端」在任何会话里都可用；Agent 会话切换 Local/Remote 目标时，
> 工作区会自动跟着切到对应一侧。

工具条：**上传 / 下载 / 刷新 / 新建文件夹 / 新建文件 / 删除 / 编辑**（单栏列表，显示名称与大小）。
删除支持文件与目录（递归）；编辑仅本地 / 远端，串口模式不支持。

- 下载：远端 → 本地工作区（自动避免重名）
- 上传：本地文件 → 远端当前目录
- 单文件上限 16 MiB

---

## 6. 内置 Agent 工具清单

工具由内置 Kit 提供（Host / Network / Serial / SSH / SFTP / Workspace），可在
「工具 → Kits 管理」中启用/禁用；禁用的 Kit 不暴露工具。Agent 在它们之上做
function-calling（只读工具自动执行，**修改性工具默认需用户确认**）：

| 工具 | 作用 | 修改性 |
| --- | --- | --- |
| `local_info` | Local（运行 EdgeKit 的本机）信息 | 否 |
| `local_exec` | 在 **Local**（本机）执行 shell 命令 | 是 |
| `net_ping` / `net_check_port` / `net_resolve` | Ping / TCP 端口 / DNS（从 Local 发起） | 否 |
| `serial_status` / `serial_read` | 串口状态 / 读取最近接收文本 | 否 |
| `serial_write` | 向串口发送文本 | 是 |
| `serial_exec` | 通过**串口**在设备执行命令并抓回显 | 是 |
| `ssh_status` / `ssh_exec` | SSH 状态 / 在 **Remote** 执行命令 | 否 / 是 |
| `sftp_status` / `sftp_list` / `sftp_download` | 远端状态 / 列目录 / 下载到工作区 | 否 |
| `sftp_upload` | 上传文本到远端 | 是 |
| `workspace_list` / `workspace_read` | 列 / 读本地工作区 | 否 |
| `workspace_write` | 写本地工作区（暂存固件、脚本） | 是 |

未配置模型时，内置 Agent 走 **Normal 模式**（内置流程）：巡检、系统日志、磁盘、内存、进程、系统版本、ping。
配置模型（OpenAI 兼容）后即可自然语言驱动。

### Agent 大脑可插拔（内置 / ACP）

「会话设置 → Agent 大脑」可切换大脑，工具面与审批不变：

| 选择 | `agent.config` | 说明 |
| --- | --- | --- |
| 内置 | `backend:"builtin"` | OpenAI 兼容 / Normal 流程（默认） |
| Hermes | `backend:"acp", acpCommand:"hermes", acpArgs:["acp"]` | 子进程 `hermes acp`，ACP 驱动 |
| OpenClaw | `backend:"acp", acpCommand:"openclaw", acpArgs:["acp"]` | 子进程 `openclaw acp`（需 Gateway） |
| 自定义 | `backend:"acp", acpCommand:"<命令>", acpArgs:[...]` | 任意 ACP Agent |

- EdgeKit 是 ACP **客户端**：`session/new` 时把自己的 `edgekit mcp` 作为 stdio MCP server 交给外部 Agent，
  因此它用的是你已连好的串口 / SSH 会话，工具调用与审批照常走 `internal/policy`。
- 外部 Agent 的 `session/request_permission` 会转成界面里的审批卡片（`agent.approve` 放行）。
- 选择持久化到 `agent-backend` / `agent-acp-command` / `agent-acp-args`。
- **进程常驻 + 多会话**：`hermes acp` 只启动一次并常驻；`agent.session.new` / `agent.session.list` / `agent.session.load`
  在同一进程上新建/列出/恢复会话，不重启 Agent。

### 执行目标：Local / Remote

左侧「会话信息」面板有 **Local | Remote** 切换（默认 **Remote**），按**运行位置**决定指令作用在哪一侧：

| 目标 | 语义 | 执行方式 |
| --- | --- | --- |
| **Remote**（默认） | 通过 SSH / 串口访问的另一台设备 | SSH 已连 → `ssh_exec`；否则串口已开 → `serial_exec`；都没有 → 提示先连接 |
| **Local** | 运行 EdgeKit 的本机（EdgeKit 可部署在端侧 Linux 上） | `local_exec` |

> 用「位置」而非「设备身份」命名：EdgeKit 可能就跑在被调试的端侧设备上，
> 此时 Local 就是该设备本身，不存在 "Host vs Edge" 的对立。

- Normal 模式：磁盘/内存/日志/进程/系统版本/巡检都按当前目标分流；
- AI 模式：目标会写进 system prompt，模型默认按它执行，
  但用户若明确说「本机 / local」或「远端 / 设备 / remote」，以用户所说为准；
- 切换目标会立即生效并持久化（`agent-target`，取值 `local` / `remote`；旧的 `host` / `edge` 仍兼容）；
- `net_ping` / `net_check_port` / `net_resolve` 固定从 **Local** 发起（诊断本机到目标的连通性）。

配置持久化在 `~/.config/edgekit/settings.json`（权限 0600，含 API Key）。启动时会自动加载，因此
**外部 Agent 即使不开界面也能用内置 Agent**。

```json
{
  "in-agent-base": "https://api.openai.com/v1",
  "in-agent-model": "gpt-4o-mini",
  "in-agent-key": "sk-...",
  "chk-agent-auto": false,
  "agent-target": "remote"
}
```

---

## 6.5 MCP 接入（外部 Agent 驱动 EdgeKit）

EdgeKit 也能作为 MCP 工具服务被外部 Agent 调用，工具面与内置 Agent 完全一致：

```bash
./build/edgekit                                          # 先启动 App
openclaw mcp add edgekit --command "$PWD/build/edgekit" --arg mcp
openclaw mcp probe edgekit                               # 18 tools
```

- 传输：stdio（`edgekit mcp`），协议 JSON-RPC 2.0；调用会转发给正在运行的 App
- 只读工具直接执行；**修改性工具在 App 界面弹出审批**（策略门），拒绝则返回错误文本
- App 未运行时桥接报错（复用 App 的会话，不自己开串口）

## 7. WebSocket 协议速查

连接：`ws://127.0.0.1:<port>/ws`（URL 见启动日志）。
所有消息为 `{"type": "...", "payload": {...}}`；二进制数据统一 **base64**。

### 请求（客户端 → 后端）

所有设备相关请求都带 `sessionId`（`serial.open` / `ssh.connect` 时由服务端返回）。

| type | payload |
| --- | --- |
| `session.focus` / `session.close` | `sessionId` |
| `serial.list` | – |
| `serial.open` | `{port, baud, dataBits, parity, stopBits}` |
| `serial.close` | – |
| `serial.write` | `{data, hex}` |
| `ssh.connect` | `{host, port, user, password, privateKey, passphrase}` |
| `ssh.disconnect` | – |
| `ssh.exec` | `{command}` |
| `ssh.shell.start` | `{cols, rows}` |
| `ssh.shell.write` | `{data}` |
| `ssh.shell.resize` | `{cols, rows}` |
| `ssh.shell.close` | `sessionId` |
| `agent.send` | `{text}` |
| `agent.config` | `{baseUrl, apiKey, model, autoRun, target: "local"\|"remote", backend: "builtin"\|"acp", acpCommand, acpArgs, acpOverride, acpModel}` |
| `agent.approve` | `{id, allow}` |
| `agent.cancel` / `agent.reset` | – |
| `agent.session.new` | 新建会话（常驻进程内） |
| `agent.session.list` | → `agent.sessions {sessions, current}` |
| `agent.session.load` | `{sessionId}` 恢复历史会话 |
| `fs.list` | `{side: "local"\|"remote"\|"serial", path}` |
| `fs.mkdir` / `fs.newfile` | `{side, path}` |
| `fs.upload` | `{dir, name, data}`（本地 → 远端） |
| `fs.download` | `{path}`（远端 → 本地工作区） |
| `settings.set` | 任意键值（持久化） |

### 事件（后端 → 客户端）

| type | 说明 |
| --- | --- |
| `serial.ports` | `{ports: [{name, description, vid, pid, serialNumber, isUSB}]}` |
| `sessions` | `{sessions: [{id, kind, label, connected, shell, sftp, config}], focus}` |
| `session.opened` / `session.closed` / `session.focus` | `{id, ...}` / `{id, focus}` / `{id}` |
| `serial.status` | `{sessionId, open, config}` |
| `serial.event` | `{sessionId, direction: "rx"\|"tx"\|"info"\|"error", data(base64), time}` |
| `ssh.status` | `{sessionId, connected, shell, sftp, config}` |
| `ssh.event` | `{sessionId, kind: "info"\|"stdout"\|"stderr"\|"error"\|"closed", data(base64), time}` |
| `fs.files` | `{side, path, display, entries: [{name, size, isDir, mode, time}]}` |
| `fs.done` | `{op: "upload"\|"download"\|"mkdir"\|"newfile", side, path, size?, remote?}` |
| `agent.event` | `{kind: "user"\|"assistant"\|"tool"\|"approval"\|"status"\|"error"\|"done", text?, tool?, args?, result?, state?, id?, time}` |
| `error` | `{message}` |

> `agent.event` 的 `approval` 会带 `id`；回复 `agent.approve {id, allow:true}` 才执行。

### 最小可运行示例（Node 22+，自带 WebSocket）

保存为 `edgekit-serial.mjs`，抓 `/dev/ttyUSB0` 的启动日志：

```js
const URL = process.env.EDGEKIT_WS || "ws://127.0.0.1:8799/ws";
const port = process.argv[2] || "/dev/ttyUSB0";
const ws = new WebSocket(URL);
const send = (type, payload) => ws.send(JSON.stringify({ type, payload }));
const dec = (b64) => new TextDecoder().decode(Uint8Array.from(atob(b64), (c) => c.charCodeAt(0)));

ws.onopen = () => send("serial.open", { port, baud: 115200, dataBits: 8, parity: "none", stopBits: 1 });
ws.onmessage = (e) => {
  const m = JSON.parse(e.data);
  if (m.type === "serial.event" && m.payload.direction === "rx") process.stdout.write(dec(m.payload.data));
  if (m.type === "serial.status") console.error("serial:", JSON.stringify(m.payload));
  if (m.type === "error") console.error("error:", m.payload.message);
};
setTimeout(() => { send("serial.close"); process.exit(0); }, 8000);
```

```bash
node edgekit-serial.mjs /dev/ttyUSB0
```

抓到的原始输出可能含 ANSI 转义，交给终端渲染或自行剥离。

### SSH 执行命令示例

```js
ws.onopen = () => {
  ws.send(JSON.stringify({ type: "ssh.connect", payload: {
    host: "192.0.2.10", port: 22, user: "root", privateKey: keyPem,
  }}));
  setTimeout(() => ws.send(JSON.stringify({ type: "ssh.exec", payload: { command: "uname -a; df -h" }})), 1000);
};
// 收集 ssh.event(kind=stdout) 的 base64 文本即为命令输出
```

---

## 8. 典型调试流程

**A. 抓串口启动日志**
1. `serial.list` 确认设备名 → `serial.open`（115200 8N1）
2. 复位板子，持续收集 `serial.event` 中 `direction=rx` 的文本
3. 结束时 `serial.close`

> 提示：Agent 里的磁盘/内存/日志等指令受 **Local / Remote** 目标影响，
> 想看本机就切到 Local，想看板子保持 Remote（需 SSH 或串口已连）。

**B. 登录板子看系统信息**
1. `ssh.connect`（私钥更稳）→ 自动起 Shell
2. `ssh.exec {command:"uname -a; cat /etc/os-release; df -h; free -m"}`
3. 或 `agent.send {text:"巡检设备状态"}` 让内置 Agent 汇总

**C. 收集日志/文件到本机**
1. SSH 连接后 `fs.list {side:"remote", path:"/var/log"}`
2. `fs.download {path:"/var/log/messages"}` → 落到 `~/EdgeKit/workspace/`

**D. 上传固件/配置**
1. 本地先放到工作区：`workspace_write` 或界面「工作区 → 本地」新建
2. 「工作区 → 本地」选文件 → **上传** → 写入远端当前目录
3. `ssh.exec` 执行刷写命令（修改性，会请求确认）

**E. 串口命令交互（设备无网络时）**
1. `serial.open` 后 `serial.write {data:"ls /\\r\\n"}`
2. 读取 `serial.event` 的 rx 文本

**F. 让内置 Agent 自主排障**
1. `agent.send {text:"板子起不来，先看串口输出，再尝试 SSH 登录排查，最后给结论"}`
2. 监听 `agent.event`：`tool` 显示调用了什么、`approval` 需要 `agent.approve` 放行

---

## 9. 安全与注意事项

- 内部 WebSocket **仅监听回环地址**，无鉴权；不要 `-addr 0.0.0.0`。
- SSH 主机密钥不校验，只打印指纹；不要用于不可信网络。
- Agent 的修改性工具（写串口、执行命令、上传/写文件）**默认需要确认**；
  `chk-agent-auto=true` 会跳过确认，自动化时请自行评估风险。
- `~/.config/edgekit/settings.json` 明文保存 API Key（0600）。
- 单文件传输上限 16 MiB；串口无流量控制，大文件请用 SSH/SFTP。
- 串口「工作区」是 `ls -la` 抓取，**不可靠**：可能受提示符、颜色、busybox 差异影响。

---

## 10. 排错

| 现象 | 处理 |
| --- | --- |
| 打不开串口 / Permission denied | 加入 `dialout` 组并重新登录 |
| 串口打开失败 `device busy` | 关闭其它占用进程（`fuser -v /dev/ttyUSB0`） |
| 连不上 SSH | 确认端口/凭据；`net_check_port {host, port:22}` 先探端口 |
| WebSocket 连不上 | 用固定端口启动 `./build/edgekit -addr 127.0.0.1:8799`，看启动日志打印的地址 |
| 无图形环境 | `xvfb-run -a ./build/edgekit -addr 127.0.0.1:8799` 后只用 WebSocket API |
| 链接报 `GLIBCXX_3.4.30` | 用 `make build`（已内置 libstdc++ shim），不要裸 `go build` |
| 中文显示为方块 | `sudo apt install fonts-noto-cjk` |
