#!/bin/sh
# EdgeKit self-extracting installer.
#
#   EdgeKit-<version>.run                  install (uses /usr/local as root,
#                                          ~/.local otherwise)
#   EdgeKit-<version>.run --prefix /opt    install under another prefix
#   EdgeKit-<version>.run --check          only run the dependency check
#   EdgeKit-<version>.run --force          install even if deps are missing
#   EdgeKit-<version>.run --uninstall      remove an installed copy
#
# The payload (binary, desktop entry, icon, dependency checker) is the gzipped
# tar appended after the __ARCHIVE_BELOW__ marker.
set -e

VERSION="@VERSION@"
WEBKIT="@WEBKIT@"

PREFIX=""
MODE="install"
FORCE=0

usage() {
	cat <<EOF
EdgeKit $VERSION 安装器（WebKitGTK $WEBKIT）

用法: $0 [选项]
  --prefix DIR    安装到 DIR（默认：root 用 /usr/local，普通用户用 ~/.local）
  --check         只做运行时依赖检查，不安装
  --force         依赖缺失时仍然安装
  --uninstall     卸载
  -h, --help      显示本帮助
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--prefix)
		[ $# -ge 2 ] || { echo "错误：--prefix 需要一个目录" >&2; exit 2; }
		PREFIX="$2"
		shift 2
		;;
	--check)
		MODE="check"
		shift
		;;
	--force)
		FORCE=1
		shift
		;;
	--uninstall)
		MODE="uninstall"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "错误：未知参数 $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

if [ -z "$PREFIX" ]; then
	if [ "$(id -u)" = "0" ]; then
		PREFIX=/usr/local
	else
		PREFIX="$HOME/.local"
	fi
fi

BIN="$PREFIX/bin/edgekit"
DESKTOP="$PREFIX/share/applications/edgekit.desktop"
ICON="$PREFIX/share/icons/hicolor/scalable/apps/edgekit.svg"
LIBDIR="$PREFIX/lib/edgekit"

refresh_caches() {
	if command -v update-desktop-database >/dev/null 2>&1; then
		update-desktop-database -q "$PREFIX/share/applications" 2>/dev/null || true
	fi
	if command -v gtk-update-icon-cache >/dev/null 2>&1; then
		gtk-update-icon-cache -q -t -f "$PREFIX/share/icons/hicolor" 2>/dev/null || true
	fi
}

if [ "$MODE" = "uninstall" ]; then
	rm -f "$BIN" "$DESKTOP" "$ICON" "$LIBDIR/check-deps.sh" "$LIBDIR/variant" \
		"$PREFIX/share/doc/edgekit/copyright"
	rmdir "$LIBDIR" 2>/dev/null || true
	refresh_caches
	echo "EdgeKit 已从 $PREFIX 卸载。"
	exit 0
fi

# Locate this file and the appended archive.
SELF=$0
[ -e "$SELF" ] || SELF=$(command -v "$SELF") || true
ARCHIVE_LINE=$(awk '/^__ARCHIVE_BELOW__$/{print NR + 1; exit}' "$SELF")

TMP=$(mktemp -d "${TMPDIR:-/tmp}/edgekit.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
tail -n +"$ARCHIVE_LINE" "$SELF" | tar xzf - -C "$TMP"

echo "EdgeKit $VERSION 安装器（WebKitGTK $WEBKIT）"
echo

echo "== 运行时依赖检查 =="
if EDGEKIT_WEBKIT="$WEBKIT" sh "$TMP/check-deps.sh"; then
	:
else
	if [ "$MODE" = "check" ]; then
		exit 1
	fi
	if [ "$FORCE" = "1" ]; then
		echo "  （--force：忽略缺失依赖继续安装）"
	else
		echo
		echo "安装已中止：请先补齐上面的依赖，或使用 --force 继续。" >&2
		exit 1
	fi
fi
echo

if [ "$MODE" = "check" ]; then
	echo "依赖检查完成（未安装）。"
	exit 0
fi

echo "安装到 $PREFIX …"
install -Dm755 "$TMP/edgekit" "$BIN"
install -Dm644 "$TMP/edgekit.desktop" "$DESKTOP"
install -Dm644 "$TMP/edgekit.svg" "$ICON"
install -Dm755 "$TMP/check-deps.sh" "$LIBDIR/check-deps.sh"
install -Dm644 "$TMP/variant" "$LIBDIR/variant"
install -Dm644 "$TMP/LICENSE" "$PREFIX/share/doc/edgekit/copyright"

# Point the launcher at the absolute binary/icon so the prefix does not need to
# be on PATH or in an icon theme search path.
sed -i -e "s|^Exec=.*|Exec=$BIN|" -e "s|^Icon=.*|Icon=$ICON|" "$DESKTOP"

refresh_caches

cat <<EOF

安装完成。
  · 启动：$BIN   （无图形环境用：$BIN -headless）
  · 卸载：$0 --uninstall --prefix $PREFIX
EOF

case ":$PATH:" in
*":$PREFIX/bin:"*) ;;
*)
	echo "  · $PREFIX/bin 不在 PATH 中，可在 ~/.profile 里加入："
	echo "      export PATH=\"$PREFIX/bin:\$PATH\""
	;;
esac

cat <<'EOF'

串口权限（把当前用户加入 dialout 组后重新登录，只需一次）：
  sudo usermod -aG dialout "$USER"

接入外部 AI Agent（需先启动 EdgeKit）：
  openclaw mcp add edgekit --command edgekit --arg mcp
EOF

exit 0

__ARCHIVE_BELOW__
