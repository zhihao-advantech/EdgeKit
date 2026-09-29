#!/usr/bin/env bash
# Cross-compile the EdgeKit Windows (amd64) executable.
#
# cgo is required because the window is provided by the WebView2 native layer,
# so a Windows C/C++ toolchain is needed. Provide one of:
#   * `zig` in PATH (uses `zig cc -target x86_64-windows-gnu`), or
#   * x86_64-w64-mingw32-gcc / -g++ in PATH, or
#   * explicit CC / CXX environment variables.
#
# Optional Authenticode signing (avoids the "unknown publisher" SmartScreen
# warning on managed machines). Set WINDOWS_PFX to a .pfx/.p12 code-signing
# certificate and the script signs the exe with osslsigncode (Linux/macOS) or
# signtool (Windows) when available:
#   WINDOWS_PFX=my.pfx WINDOWS_PFX_PASS=secret \
#   WINDOWS_TIMESTAMP_URL=http://timestamp.digicert.com packaging/build-windows.sh
#
# Usage: packaging/build-windows.sh [output.exe]
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-$root/dist/edgekit.exe}"
version="${VERSION:-0.1.0}"

if [[ -z "${CC:-}" ]]; then
	if command -v zig >/dev/null 2>&1; then
		CC="zig cc -target x86_64-windows-gnu"
		CXX="zig c++ -target x86_64-windows-gnu"
	elif command -v x86_64-w64-mingw32-gcc >/dev/null 2>&1; then
		CC="x86_64-w64-mingw32-gcc"
		CXX="x86_64-w64-mingw32-g++"
	else
		echo "缺少 Windows C/C++ 工具链：请安装 zig 或 mingw-w64，或用 CC/CXX 指定。" >&2
		exit 1
	fi
fi
: "${CXX:?请同时设置 CXX}"

mkdir -p "$(dirname "$out")"
cd "$root"

# -H=windowsgui keeps the console hidden; the explicit --subsystem flag is
# needed because cgo links through the external (gcc) linker. CGO_LDFLAGS is
# cleared so a Linux-only -L path from the host Makefile cannot leak in.
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="$CC" CXX="$CXX" CGO_LDFLAGS= \
	"${GO:-go}" build -trimpath \
	-ldflags "-H=windowsgui -extldflags=-Wl,--subsystem,windows -s -w -X main.appVersion=$version" \
	-o "$out" ./cmd/edgekit

echo "已生成 $out ($version)"

# Optional Authenticode signature (see the header). Needs a code-signing cert;
# without one the exe is unsigned and Windows shows "unknown publisher".
if [[ -n "${WINDOWS_PFX:-}" ]]; then
	name="${WINDOWS_SIGN_NAME:-EdgeKit}"
	url="${WINDOWS_SIGN_URL:-}"
	ts="${WINDOWS_TIMESTAMP_URL:-}"
	pass="${WINDOWS_PFX_PASS:-}"

	if command -v osslsigncode >/dev/null 2>&1; then
		args=(sign -pkcs12 "$WINDOWS_PFX" -pass "$pass" -n "$name")
		[[ -n "$url" ]] && args+=(-i "$url")
		[[ -n "$ts" ]] && args+=(-ts "$ts")
		osslsigncode "${args[@]}" -in "$out" -out "$out.signed"
		mv "$out.signed" "$out"
		echo "已用 osslsigncode 签名 $out"
	elif command -v signtool >/dev/null 2>&1 || command -v signtool.exe >/dev/null 2>&1; then
		args=(sign /fd SHA256 /f "$WINDOWS_PFX")
		[[ -n "$pass" ]] && args+=(/p "$pass")
		[[ -n "$ts" ]] && args+=(/tr "$ts" /td SHA256)
		command -v signtool >/dev/null 2>&1 && st=signtool || st=signtool.exe
		"$st" "${args[@]}" "$out"
		echo "已用 signtool 签名 $out"
	else
		echo "已设置 WINDOWS_PFX，但未找到 osslsigncode/signtool，跳过签名。" >&2
		exit 1
	fi
fi
