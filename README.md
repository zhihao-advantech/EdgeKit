# EdgeKit

English · [中文](README.zh-CN.md)

**AI-accelerated edge device upgrades.**

EdgeKit puts everything you need to debug and upgrade edge devices into one
native desktop app, centred on an **Agent session**: an AI agent, a serial
console, an SSH terminal and an SFTP workspace — with **several boards connected
at once**.

The UI is written in HTML/CSS/JS and **embedded in the executable**, opened by
WebKit2GTK in a native window — no browser required. The backend is Go; the two
talk over a loopback JSON / WebSocket protocol.

## Features

- **Agent session**: drive serial / SSH / workspace in natural language. The
  brain is either built-in (OpenAI-compatible function calling; Normal built-in
  workflows when no model is configured) or an external ACP agent
  (Hermes / OpenClaw). Mutating operations prompt for confirmation by default.
- **Serial**: auto-enumerates ports, configurable baud / data bits / parity /
  stop bits, live traffic with timestamps, HEX view and byte counters.
- **SSH**: password or key auth, interactive shell (PTY, resize), host key
  fingerprint shown.
- **Workspace**: local sandbox `~/EdgeKit/workspace` and remote SFTP; upload,
  download, edit online (single file ≤ 16 MiB).
- **Multi-board**: connect several boards at once; `sessions_list` lists the
  sessions and any device tool accepts a `session` argument to pick a board.
- **Kits** (capability packs): `Host` / `Network` / `Serial` / `SSH` / `SFTP` /
  `Workspace` / `CodeEdit` / `Timeline` / `Sessions` — 25 tools in total.
  Enable/disable them under "Tools → Kits"; device-dependent kits stay hidden
  until a matching device is connected.
- **MCP**: `edgekit mcp` exposes the same tool surface to external agents such
  as OpenClaw and Claude Code.

## Requirements

- Go 1.22+
- GTK 3 and WebKit2GTK development libraries (4.0 on Ubuntu 22.04 / Debian 12;
  4.1 on Ubuntu 24.04+ / Debian 13+):

```bash
# Ubuntu 22.04 / Debian 12
sudo apt install -y build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.0-dev
# Ubuntu 24.04+ / Debian 13+
sudo apt install -y build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev
```

Serial access (add your user to `dialout`, then log in again):

```bash
sudo usermod -aG dialout "$USER"
```

## Build and run

```bash
make build          # produces build/edgekit
./build/edgekit     # opens the window
make run            # go run
make headless       # backend only, no window (servers / remote)
```

The WebKit variant is auto-detected via `pkg-config`, or set explicitly:

```bash
make WEBKIT=4.1 build   # force the 4.1 variant
make info               # show version and WebKit variant to be built
```

| Flag | Description |
| --- | --- |
| `-addr` | internal WebSocket listen address, default `127.0.0.1:0` (UI only) |
| `-debug` | open the WebView developer tools |
| `-headless` | backend only, no window |

> If the link fails with `GLIBCXX_3.4.30`, `make` already works around it; with
> plain `go build` pass the same `CGO_LDFLAGS`.

## Packaging and installation

Both packages **verify the required system package versions**:

```bash
make package-deb    # dist/edgekit_<version>_<arch>.deb
make package-run    # dist/EdgeKit-<version>-webkit<4.0|4.1>-<arch>.run
make package        # both
```

```bash
sudo apt install ./dist/edgekit_<version>_<arch>.deb   # apt resolves dependencies

./dist/EdgeKit-<version>-webkit<4.0|4.1>-<arch>.run    # root → /usr/local, else ~/.local
./EdgeKit-*.run --check                                # dependency check only
./EdgeKit-*.run --uninstall                            # remove
```

> WebKitGTK 4.0 and 4.1 are ABI-incompatible, so build per target:
> `WEBKIT=4.0` on 22.04 / Debian 12, `WEBKIT=4.1` on 24.04+ / Debian 13+.

## Usage

1. Run `edgekit`: a menu bar on top, "Sessions / Workspace / Session settings"
   on the left, session tabs and a black terminal; the **gear icon** at the top
   right holds the terminal display settings (auto-scroll / timestamps / HEX /
   local echo), saved automatically and applied globally.
2. New session: "Session → New Serial / SSH session"; connect several boards and
   switch tabs.
3. Use the agent: fill in an OpenAI-compatible Base URL / API key / model under
   "Session settings → Agent", or switch the agent brain to Hermes / OpenClaw.
   Mutating operations prompt for confirmation in the UI.
4. Workspace: upload / download / edit files in the left panel; once SSH is
   connected the remote side is available.
5. Connect an external agent (start the app first):

```bash
openclaw mcp add edgekit --command edgekit --arg mcp
openclaw mcp probe edgekit        # lists the tools (varies with connected devices)
```

Settings live in `~/.config/edgekit/settings.json` (contains the API key,
mode 0600).

### Test sessions

Create one from "Test sessions → +" in the session list, or the "测试会话" tab in
the tab strip: the right pane drives four steps,
**connect → run → generate → archive**. A case is a list of commands with
expected output (regexp), an optional timeout and exit-code check. Cases can be
saved to the workspace as `tests/<name>.test.json` (edit the file directly and
reload it, too), and results plus a Markdown report are archived under
`~/EdgeKit/workspace/tests/runs/<id>/`; a failed run is still reported and
archived.

### Let an agent debug boards itself

See [`skills/edgekit-board-debug/SKILL.md`](skills/edgekit-board-debug/SKILL.md);
install it as an agent skill:

```bash
ln -s "$PWD/skills/edgekit-board-debug" ~/.agents/skills/edgekit-board-debug
```

## Layout

```
cmd/edgekit/            entry point (WebView + server startup)
internal/server/        HTTP + WebSocket service and embedded frontend assets
internal/kit/ kits/     Kit model and built-in capability packs
internal/agent/         AI agent (built-in / ACP)
internal/{serial,sshclient,sftpx,workspace,timeline,policy,mcp,acp,netdiag}/
packaging/              .deb / .run scripts and dependency check
third_party/webview_go/ webview fork with WebKit 4.0/4.1 build tags
```

## Security notes

- SSH host keys are not verified, only fingerprinted; do not use on untrusted
  networks.
- The internal WebSocket listens on loopback only, never on the LAN.
- Mutating tools ask for confirmation each time; enable "auto-run" with care.

## License

Apache License 2.0, see [LICENSE](LICENSE).
