# EdgeKit

English · [中文](README.zh-CN.md)

**AI-accelerated edge-device upgrades.**

An **Agent session** is the hub: it brings everything needed to debug and upgrade
edge devices into one native desktop app — AI agent, serial console, SSH terminal
and workspace file management — and can talk to **several boards at once**.

The UI is written in HTML/CSS/JS and **embedded in the executable**. Running the
program opens it directly in a native window through **WebKit2GTK**
(`libwebkit2gtk-4.0`) — no browser, no URL to visit. Go provides the backend, and
the two communicate over a loopback JSON / WebSocket protocol.

```
┌──────────────────────────────────────┐        ┌────────────────────────────────┐
│  WebView (WebKit2GTK)                │  ws:// │  Go backend                    │
│  menu / sessions / tabs / Agent      │ <────> │  internal/server               │
└──────────────────────────────────────┘        │   ├── agent     agent hub      │
                                                │   ├── serial    serial         │
                                                │   ├── sshclient SSH terminal   │
                                                │   ├── sftpx     remote files   │
                                                │   ├── workspace local workspace│
                                                │   └── netdiag   network checks │
                                                └────────────────────────────────┘
```

## Kit architecture

Capabilities are provided as **Kits**; the host aggregates the tools each Kit
contributes, and **the built-in agent, the UI protocol and the future MCP server
share one single definition**:

- Built-in Kits: `Host`, `Network`, `Serial`, `SSH`, `SFTP`, `Workspace` — 18 tools in total
- Each Kit carries a `Manifest` (`id` / `name` / `version` / `license` / `runtime` / `activation`)
  and a set of `Tool`s (JSON Schema + risk level `read` / `mutate` / `dangerous`)
- The host requires approval for non-`read` tools (reusing the existing approval flow)
- "Help → About" shows the loaded Kits and tool counts

Code layout:

```
internal/kit/    Kit / Manifest / Tool / Registry and capability interfaces (Serial / SSH / SFTP)
internal/kits/   built-in Kit implementations; Builtin(deps) returns them all
internal/agent/  consumes only kit.Registry, no hard-coded tools
```

Next phases: external `kit.json` manifests, external Kits over MCP, and a Kits management page.

## Agent + Kit

EdgeKit is shaped as **Agent (orchestration/dialogue) + Kit (capability packs)**,
isomorphic to VS Code extensions, TRAE's per-agent tool toggles and WorkBuddy
connectors:

- **Kit**: a capability pack declaring a `Manifest`
  (`id` / `version` / `license` / `runtime` / `activation`) and a set of risk-tagged
  `Tool`s (`read` / `mutate` / `dangerous`)
- **Agent**: consumes the Kits' `Tool`s and defines no tools itself; the brain can
  be built-in (Normal macros / OpenAI-compatible) or external (MCP → OpenClaw)
- **Enable/disable**: "Tools → Kits…" or "Kits…" in the Agent panel
  - A disabled Kit **does not expose tools to the agent or MCP and cannot run**
    (calls return "unknown tool")
  - The panel shows each Kit's version, license, activation events, tools and risk levels
  - The choice is persisted to `kits.disabled` in `~/.config/edgekit/settings.json`
- **Attribution**: tool cards in the transcript show which Kit they came from
  (e.g. `Serial · serial_read`)

## Pluggable agent brain (built-in / ACP: Hermes, OpenClaw)

The agent "brain" can be switched; the tool surface and approval flow stay the same:

- **Built-in** (default): an OpenAI-compatible function-calling loop; with no model
  configured it falls back to the Normal built-in workflows.
- **External ACP agent**: launches `hermes acp` or `openclaw acp` as a child process
  and drives it over **ACP (Agent Client Protocol, JSON-RPC 2.0 over stdio)**.
  EdgeKit acts as the ACP **client** and, at `session/new`, hands the peer its own
  `edgekit mcp` as a stdio MCP server, so the external agent still uses the connected
  serial / SSH sessions and the same Kit tools, and mutating calls still raise an
  approval card in the EdgeKit UI (`session/request_permission` reuses the
  `internal/policy` gate).

To switch: pick `Built-in / Hermes / OpenClaw / Custom ACP…` in
"Session settings → Agent brain"; for Custom, enter the full command (e.g.
`hermes acp`). The choice is persisted to `~/.config/edgekit/settings.json`.

```
Agent panel ──agent.config{backend, acpCommand, acpArgs}──▶ internal/agent
   ├── builtin  current OpenAI-compatible / Normal flow
   └── acp      internal/acp client ──stdio──▶ hermes acp / openclaw acp
                        │ session/new{mcpServers:[edgekit mcp]}
                        ▼
                   edgekit mcp (reuses the Kit Registry + policy gate)
```

> Note: `openclaw acp` is backed by the Gateway and needs it running;
> `hermes acp` runs standalone. The external agent's model, memory and Skills are
> its own; EdgeKit's model panel only applies to the built-in brain.

**Resident process + multiple sessions**: `hermes acp` is lazily started once and
lives as long as the EdgeKit process; "New session / History" go through ACP
`session/new`, `session/list` and `session/load` on the same process, without
restarting the agent. The selection is persisted in the UI; model switching goes
through `session/set_model`, also without a restart.

## Device model and timeline

Every observation/action goes into the device's **timeline** (append-only, with
sequence numbers and timestamps). It sits between providers and consumers and is
the **single source of truth** shared by the UI, the built-in agent and (later) MCP:

- Location: `internal/timeline/`
- API: `Append` / `Since(seq, limit)` / `Wait(ctx, filter, timeout)` / `LastSeq`
- The host keeps one timeline per device session (capped at 5000 records); provider
  events are stored before they are broadcast
- Uses: **UI replay** (restore scrollback after a reload), cross-channel
  correlation, the future `wait_for_output`, and auditing
- Protocol: `timeline` request → `timeline.records` event (fields `seq/time/channel/kind/data`)

## MCP (OpenClaw / Claude Code)

EdgeKit can be driven by an external agent as an **MCP tool server**: `edgekit mcp`
speaks MCP over stdio and forwards every call to the **running app**, so the agent
uses the serial / SSH sessions you already have connected and the in-app approval
flow still applies.

```bash
# 1) start the app first (it publishes its endpoint to ~/.config/edgekit/runtime.json)
./build/edgekit

# 2) add it to OpenClaw (add probes the connection first and only saves on success)
openclaw mcp add edgekit --command "$PWD/build/edgekit" --arg mcp
openclaw mcp probe edgekit        # -> edgekit: 18 tools
```

- Protocol: MCP (JSON-RPC 2.0 over stdio), implementing `initialize` /
  `tools/list` / `tools/call`
- Tools come from the Kit Registry: **read-only tools run directly; mutating tools
  are gated by `internal/policy`, which raises an approval in the app UI** — the
  same gate the built-in agent uses
- Other MCP clients (Claude Code / Codex CLI / Goose…) use the same stdio command
  `edgekit mcp`
- If the app is not running the bridge reports a clear error — it reuses the app's
  sessions and never grabs a serial port itself

## Agent board debugging

To have an agent (rather than a human) use EdgeKit for serial / SSH board
debugging, see [`skills/edgekit-board-debug/SKILL.md`](skills/edgekit-board-debug/SKILL.md):
building and launching, the capability list, built-in agent tools, a WebSocket
protocol cheat sheet and typical debugging flows.

Install it as an agent skill:

```bash
ln -s "$PWD/skills/edgekit-board-debug" ~/.agents/skills/edgekit-board-debug
```

## UI

The layout follows **MobaXterm**: a top menu bar + a left
"Sessions / Workspace / Session settings" column + session tabs + a black terminal.

- **Menu bar**: `Session`, `Terminal`, `Tools`, `View`, `Help`; click to open a
  submenu, moving to an adjacent menu switches automatically, `Esc` or a click
  outside closes it.
  - `Session`: new Agent / serial / SSH session, close current / all
  - `Terminal`: auto-scroll, timestamps, HEX view, local echo (checkable), clear, copy, paste
  - `Tools`: refresh the serial list, agent inspection / view logs
  - `View`: show/hide the sessions panel and workspace, terminal font size
  - `Help`: about EdgeKit
- **Left "Sessions" panel**: the Agent session plus several **device sessions**
  (serial / SSH; several boards at once, "＋" on the right to add), with status LEDs;
  click to switch.
- **Top tabs**: one closable tab per device session.
- **"Workspace" panel** (between sessions and settings): switches meaning with the
  current session — the Agent follows the execution target (Local → local workspace,
  Remote → remote SFTP), serial shows an experimental device directory (captured
  with `ls -la`), SSH shows both "local / remote" sides; as long as SSH is connected
  the remote side is available in any session, with upload and download.
- **"Session settings"**: switches with the current session.
- **Session tabs**: one closable tab per session; closing a tab disconnects it.
- **Terminal area**: a black terminal; the status bar at the bottom shows connection
  state and byte counters.

## Features

### Serial debugging
- Enumerates serial devices automatically (`/dev/ttyUSB*`, `/dev/ttyACM*`,
  `/dev/ttyS*`), showing VID/PID/serial number
- Configurable baud rate, data bits, parity, stop bits
- Live RX/TX log with timestamps, auto-scroll and HEX view
- Text display filters ANSI escape sequences (across chunks), with RX/TX byte counters

### SSH terminal
- Password or private-key (PEM / OpenSSH) authentication, with key passphrases
- Opens an interactive shell on connect (requests a PTY, supports window resizing)
- Shows the remote host-key fingerprint

### Agent session (AI hub)

Drives serial / SSH / workspace with natural language to complete debugging and
upgrade tasks; it is EdgeKit's core entry point.

- **Execution target Local / Remote**: switch in the left "Session info" panel which
  side commands act on (default **Remote**, the remote device); Remote goes over SSH
  (or serial if not connected), Local uses `local_exec`. In Normal mode
  disk/memory/logs/inspection follow the target; in AI mode the target is written
  into the system prompt and can be overridden explicitly.
- **Modes**:
  - **AI mode**: with an OpenAI-compatible Base URL / API Key / model configured,
    runs a function-calling loop where the model calls tools as needed and
    summarises the results;
  - **Normal mode**: with no model configured, uses built-in workflows for
    inspection, logs, disk, memory, processes, system version, ping, etc.;
  - **ACP mode**: swaps the brain for an external agent (Hermes / OpenClaw), see
    "Pluggable agent brain" above.
- **Tool set**: `local_info` / `local_exec` (local), `net_ping` / `net_check_port` /
  `net_resolve`, `serial_status` / `serial_read` / `serial_write` / `serial_exec`,
  `ssh_status` / `ssh_exec`, `sftp_status` / `sftp_list` / `sftp_download` /
  `sftp_upload`, `workspace_list` / `workspace_read` / `workspace_write`.
- **Approval**: read-only tools run automatically; **mutating operations** such as
  writing to serial, running commands or uploading files **prompt first**, and only
  run after "Allow" (tick "Auto-run mutating operations" to skip the prompt).
- **Context aware**: the agent knows whether serial / SSH is connected and the
  target, reuses established sessions, and downloads land in the local workspace.
- **Shortcuts**: device inspection / system logs / disk / memory / system version.
- Transcript, tool calls and results are kept in the panel as chat cards.

> Network diagnostics (ping / port / DNS) are no longer a separate session; they
> are part of the agent tools and can also be run directly in the SSH terminal.

### Workspace (file panel)

No longer a separate session but a persistent left-hand file panel that changes
meaning with the current session:

- **Local**: a sandbox at `~/EdgeKit/workspace` (path traversal is confined to the
  root); the agent uses it too;
- **Remote**: an SFTP subsystem is created automatically after SSH connects
  (reusing the same SSH connection, no separate credentials), with
  enter-directory / go-up;
- **Serial**: sends `ls -la` to the device to capture the directory (experimental,
  relies on the device shell);
- Toolbar: upload, download, refresh, new folder, new file, delete, edit (in-place
  text editing); a single list shows name and size, the status area shows entry
  count / selection;
- Single-file transfer limit 16 MiB.

> Serial is a raw byte stream with no file protocol, so the "serial" side only does
> an `ls` capture and offers no file transfer.

### Terminal input (PuTTY / MobaXterm style)

The terminal is itself the RX/TX buffer — **click it and type directly**, there is
no separate send bar:

- Printable characters, `Enter` (`\r`), `Backspace` (`0x7F`), `Tab`, `Esc` are sent as-is;
- Arrow keys / Home / End / Delete / PgUp / PgDn send the corresponding ANSI
  sequences, so remote history and line editing work;
- `Ctrl+A`–`Ctrl+Z` send control characters (e.g. `Ctrl+C` to interrupt),
  `Alt+<key>` sends the `ESC` prefix;
- `Ctrl+Shift+C` copies, `Ctrl+Shift+V` pastes; system paste events are supported too;
- A block cursor is shown at the end of the terminal, hollow when unfocused;
- Serial devices usually echo; if a device does not, tick "Terminal → Local echo".

The display uses a **line-oriented terminal model** (below) rather than stripping
escape characters chunk by chunk:

- Remote-echoed backspace (`\b` / `0x7F`), carriage-return redraw (`\r`), in-line
  erase (CSI `K`) and cursor left/right (CSI `C`/`D`/`G`) are parsed correctly, so
  line editing no longer garbles;
- Escape sequences split across serial chunks are stitched by a persistent state
  machine, independent of chunk boundaries;
- Screen erase (CSI `J`) and colour (CSI `m`) are not handled yet, to preserve the
  scrollback log.

> Note: this model covers shell line editing and serial logs. Full-screen programs
> such as `vim` or `top` would need a complete terminal emulator like xterm.js.

## Performance design

Two layers of optimisation for high-throughput cases (fast serial, SSH log floods):

**Backend: batching**
- Serial / SSH data events are aggregated per `(channel, direction)` and flushed
  every **12 ms** or at **32 KB**, collapsing many tiny packets into one WebSocket
  message;
- Log / lifecycle events (opened, errors, …) are not batched and **flush the
  channel's pending data first**, preserving order;
- Effect: at 115200 baud with byte-at-a-time delivery (~11520/s), downstream
  messages drop from **11520/s to ~83/s** (reproducible with
  `go test ./internal/server -bench Batcher`).

**Frontend: line buffering + per-frame coalescing**
- The current line uses an **incremental string**: appends are amortised O(1), and
  only in-line overwrites (backspace / carriage-return redraw) slice, avoiding the
  O(n²) of per-character `join()`;
- Active-line DOM writes and scrolling are coalesced to once per frame with
  `requestAnimationFrame`, so no matter how many bytes arrive in a frame the DOM is
  updated once.

Measured (Node, pure string handling):

| Case | Per-chunk concat | Per-char join+slice | Current incremental model |
| --- | --- | --- | --- |
| 20 KB single line, 1 byte/chunk | 5.6 ms | 1011 ms | **0.9 ms** |
| 1 MB, 64 chars/line | 0.7 ms | 8109 ms | **0.7 ms** |

> Batching adds at most ~12 ms of display latency, imperceptible for serial
> monitoring and SSH interaction.

## Requirements

- Go 1.22+
- GTK 3 and WebKit2GTK 4.0 development libraries:

```bash
sudo apt update
sudo apt install -y build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.0-dev
```

Serial access (add your user to the `dialout` group, then log in again):

```bash
sudo usermod -aG dialout "$USER"
```

## Build and run

```bash
make build          # produces build/edgekit
./build/edgekit     # opens the window directly

make run            # go run
```

Command-line flags:

| Flag | Description |
| --- | --- |
| `-addr` | internal WebSocket listen address, default `127.0.0.1:0` (random port, UI only) |
| `-debug` | open the WebView developer tools |

Running it pops up the app window; all UI assets are embedded, so no browser or
external files are needed. A graphical environment (X11 / Wayland) is required.

## About the `GLIBCXX_3.4.30` link error

Some distributions build `libwebkit2gtk-4.0` with a newer GCC while the `libstdc++`
in the default `g++` search path is older, which fails the link with:

```
undefined reference to `std::condition_variable::wait(...)@GLIBCXX_3.4.30'
```

The `Makefile` handles this already: it creates a symlink under `build/.libstdcxx/`
to the runtime `libstdc++.so.6` and passes `CGO_LDFLAGS=-L...` so the linker prefers
it — no system files are touched. If you use `go build` instead of `make`, pass the
same `CGO_LDFLAGS` yourself.

## Directory layout

```
cmd/edgekit/            entry point: creates the WebView and loads the local server
internal/serial/        serial manager (enumerate / open / read / write)
internal/sshclient/     SSH terminal (connect / exec / interactive shell)
internal/sshutil/       SSH connection params and dialing (shared by sshclient and sftpx)
internal/timeline/      device timeline (append-only records, shared by UI/Agent/MCP)
internal/policy/        approval gate (read / mutate / dangerous)
internal/mcp/           minimal MCP server (JSON-RPC 2.0 over stdio)
internal/runtime/       runtime endpoint publishing (so `edgekit mcp` finds the app)
internal/kit/           Kit extension model (Manifest / Tool / Registry / capability interfaces)
internal/kits/          built-in Kits (Host / Network / Serial / SSH / SFTP / Workspace)
internal/agent/         AI agent (consumes the Registry, Normal mode, model calls, approval)
internal/acp/           minimal ACP client (JSON-RPC 2.0 over stdio)
internal/netdiag/       network checks (ping / port / DNS / local info)
internal/sftpx/         SFTP browsing and transfer (attached to an SSH connection)
internal/workspace/     local workspace (sandboxed file operations)
internal/server/        HTTP + WebSocket service, connecting frontend and backend
internal/server/web/    embedded frontend assets (HTML/CSS/JS)
```

## WebSocket protocol

Requests (browser → backend):

Every device-related request carries `sessionId` (returned on connect; used to tell
multiplexed sessions apart).

| Group | type | payload |
| --- | --- | --- |
| Session | `session.focus` / `session.close` | `sessionId` |
| Serial | `serial.list` | – |
| | `serial.open` | `{port, baud, dataBits, parity, stopBits}` → new session |
| | `serial.close` | `sessionId` |
| | `serial.write` | `{data, hex}` + `sessionId` |
| SSH | `ssh.connect` | `{host, port, user, password, privateKey, passphrase}` → new session |
| | `ssh.disconnect` | `sessionId` |
| | `ssh.exec` | `{command}` |
| | `ssh.shell.start` | `{cols, rows}` |
| | `ssh.shell.write` | `{data}` |
| | `ssh.shell.resize` | `{cols, rows}` |
| | `ssh.shell.close` | – |
| Agent | `agent.send` | `{text}` |
| | `agent.config` | `{baseUrl, apiKey, model, autoRun, backend: "builtin"\|"acp", acpCommand, acpArgs, acpOverride, acpModel}` |
| | `agent.approve` | `{id, allow}` |
| | `agent.cancel` / `agent.reset` | – |
| | `agent.session.new` | new session on the resident agent process |
| | `agent.session.list` | → `agent.sessions` event `{sessions, current}` |
| | `agent.session.load` | `{sessionId}` resume a past session |
| Settings | `settings.set` | arbitrary key/value (persisted to the local config file) |
| Workspace | `fs.list` | `{side: local\|remote\|serial, path}` |
| | `fs.mkdir` / `fs.newfile` | `{side, path}` |
| | `fs.upload` | `{dir, name, data}` (local → remote) |
| | `fs.download` | `{path}` (remote → local workspace) |

Events (backend → browser): `sessions` (connect snapshot), `session.opened` /
`session.closed` / `session.focus`, `serial.ports`, `serial.status`, `serial.event`,
`ssh.status`, `ssh.event`, `fs.files`, `fs.done`, `agent.event`, `agent.config`,
`agent.models`, `agent.sessions`, `settings`, `error`. `serial.status` /
`serial.event` / `ssh.status` / `ssh.event` all carry `sessionId`. Binary data is
base64-encoded throughout.

## Security notes

- SSH host keys are not verified, only their fingerprints are printed, to ease
  debugging on intranet devices; do not use on untrusted networks.
- The internal WebSocket listens on loopback only, for the local WebView; it is not
  exposed to the LAN.
- The agent's mutating tools require per-call confirmation by default; "auto-run"
  skips it, so enable it carefully.
- UI settings (including the API Key) are stored in
  `~/.config/edgekit/settings.json` (mode 0600).

## License

EdgeKit is open source under the **Apache License 2.0**; see [LICENSE](LICENSE) for
the full text.

```
Copyright 2026 EdgeKit contributors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
```

Each Kit carries its own `license` field; external Kits will attach as separate
processes (MCP) and keep their own license.

## Roadmap

EdgeKit aims to accelerate edge-device upgrades with AI. This version covers the
general debugging toolbox; upcoming work includes AI-assisted fault diagnosis and
log analysis, automatic device discovery, and batch operations with intelligent
orchestration.
