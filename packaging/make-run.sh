#!/usr/bin/env bash
# Build a self-extracting EdgeKit .run installer.
#
# It bundles the binary, desktop entry, icon and a dependency checker, then
# appends them to a shell stub (see run/stub.sh). The installer verifies the
# runtime dependencies before installing and supports --prefix/--uninstall.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PKGDIR="$ROOT/packaging"

VERSION=${VERSION:-0.1.0}
WEBKIT=${WEBKIT:-4.0}
DIST=${DIST:-$ROOT/dist}
ARCH=${ARCH:-$(dpkg --print-architecture)}
BIN=${BIN:-$ROOT/build/edgekit}

case "$WEBKIT" in
4.1) WEBKIT_LIB="libwebkit2gtk-4.1" ;;
4.0 | *)
	WEBKIT="4.0"
	WEBKIT_LIB="libwebkit2gtk-4.0"
	;;
esac

[ -x "$BIN" ] || {
	echo "错误：找不到已构建的二进制 $BIN（先运行 make build）" >&2
	exit 1
}
if ! ldd "$BIN" 2>/dev/null | grep -q "${WEBKIT_LIB}.so"; then
	echo "错误：$BIN 未链接 ${WEBKIT_LIB}，请先 'make WEBKIT=$WEBKIT build'" >&2
	exit 1
fi

STAGE="$DIST/.stage-run"
rm -rf "$STAGE"
mkdir -p "$STAGE"

install -m755 "$BIN" "$STAGE/edgekit"
install -m644 "$PKGDIR/edgekit.desktop" "$STAGE/edgekit.desktop"
install -m644 "$ROOT/internal/server/web/icon.svg" "$STAGE/edgekit.svg"
install -m755 "$PKGDIR/deps-check.sh" "$STAGE/check-deps.sh"
printf '%s\n' "$WEBKIT" >"$STAGE/variant"
install -m644 "$ROOT/LICENSE" "$STAGE/LICENSE"

tar -czf "$STAGE/payload.tar.gz" -C "$STAGE" \
	edgekit edgekit.desktop edgekit.svg check-deps.sh variant LICENSE

mkdir -p "$DIST"
OUT="$DIST/EdgeKit-${VERSION}-webkit${WEBKIT}-${ARCH}.run"
sed -e "s/@VERSION@/$VERSION/" -e "s/@WEBKIT@/$WEBKIT/" "$PKGDIR/run/stub.sh" >"$STAGE/stub"
cat "$STAGE/stub" "$STAGE/payload.tar.gz" >"$OUT"
chmod 755 "$OUT"
rm -rf "$STAGE"

echo "已生成 $OUT"
echo "  版本:   $VERSION"
echo "  WebKit: $WEBKIT"
echo "  架构:   $ARCH"
echo "  大小:   $(du -h "$OUT" | cut -f1)"
