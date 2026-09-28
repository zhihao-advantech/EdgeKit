#!/usr/bin/env bash
# Build an EdgeKit .deb from the already-built binary.
#
# The package declares its system dependencies (Depends) so apt/dpkg resolve
# them, and its postinst prints a verified dependency table. Use WEBKIT=4.0 on
# Ubuntu 22.04 / Debian 12 and WEBKIT=4.1 on Ubuntu 24.04+ / Debian 13+.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PKGDIR="$ROOT/packaging"

VERSION=${VERSION:-0.1.0}
WEBKIT=${WEBKIT:-4.0}
DIST=${DIST:-$ROOT/dist}
ARCH=${ARCH:-$(dpkg --print-architecture)}
BIN=${BIN:-$ROOT/build/edgekit}
MAINTAINER=${MAINTAINER:-$(git -C "$ROOT" config user.name 2>/dev/null || echo "EdgeKit contributors") <$(git -C "$ROOT" config user.email 2>/dev/null || echo "edgekit@localhost")>}

case "$WEBKIT" in
4.1)
	WEBKIT_LIB="libwebkit2gtk-4.1"
	DEPENDS="libgtk-3-0 (>= 3.24), libwebkit2gtk-4.1-0 (>= 2.42), libstdc++6 (>= 12)"
	WEBKIT_DESC="WebKitGTK 4.1 (Ubuntu 24.04+, Debian 13+)"
	;;
4.0 | *)
	WEBKIT="4.0"
	WEBKIT_LIB="libwebkit2gtk-4.0"
	DEPENDS="libgtk-3-0 (>= 3.24), libwebkit2gtk-4.0-37 (>= 2.36), libstdc++6 (>= 12)"
	WEBKIT_DESC="WebKitGTK 4.0 (Ubuntu 22.04, Debian 12)"
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

# Debian version: starts with a digit; '-' is not allowed in the upstream part.
DEB_VERSION=$(printf '%s' "$VERSION" | sed -e 's/^v//' -e 's/-/+/g')
case "$DEB_VERSION" in
[0-9]*) ;;
*) DEB_VERSION="0.0.0+$DEB_VERSION" ;;
esac

STAGE="$DIST/.stage/edgekit"
rm -rf "$DIST/.stage"
mkdir -p "$STAGE/DEBIAN"

install -Dm755 "$BIN" "$STAGE/usr/bin/edgekit"
install -Dm644 "$PKGDIR/edgekit.desktop" "$STAGE/usr/share/applications/edgekit.desktop"
install -Dm644 "$ROOT/internal/server/web/icon.svg" "$STAGE/usr/share/icons/hicolor/scalable/apps/edgekit.svg"
install -Dm755 "$PKGDIR/deps-check.sh" "$STAGE/usr/lib/edgekit/check-deps.sh"
install -Dm644 /dev/stdin "$STAGE/usr/lib/edgekit/variant" <<<"$WEBKIT"
install -Dm644 "$ROOT/LICENSE" "$STAGE/usr/share/doc/edgekit/copyright"
gzip -9nc "$ROOT/README.md" >"$STAGE/usr/share/doc/edgekit/README.md.gz"
chmod 644 "$STAGE/usr/share/doc/edgekit/README.md.gz"

SIZE=$(du -sk "$STAGE" | cut -f1)

sed -e "s/@VERSION@/$DEB_VERSION/" \
	-e "s/@ARCH@/$ARCH/" \
	-e "s/@DEPENDS@/$DEPENDS/" \
	-e "s/@MAINTAINER@/$MAINTAINER/" \
	-e "s/@SIZE@/$SIZE/" \
	-e "s/@WEBKIT_DESC@/$WEBKIT_DESC/" \
	"$PKGDIR/deb/control.in" >"$STAGE/DEBIAN/control"

sed -e "s/@VERSION@/$DEB_VERSION/" "$PKGDIR/deb/postinst" >"$STAGE/DEBIAN/postinst"
sed -e "s/@VERSION@/$DEB_VERSION/" "$PKGDIR/deb/postrm" >"$STAGE/DEBIAN/postrm"
chmod 755 "$STAGE/DEBIAN/postinst" "$STAGE/DEBIAN/postrm"

OUT="$DIST/edgekit_${DEB_VERSION}_${ARCH}.deb"
mkdir -p "$DIST"
rm -f "$OUT"
dpkg-deb --build --root-owner-group "$STAGE" "$OUT" >/dev/null
rm -rf "$DIST/.stage"

echo "已生成 $OUT"
echo "  版本:   $DEB_VERSION"
echo "  架构:   $ARCH"
echo "  WebKit: $WEBKIT_DESC"
echo "  依赖:   $DEPENDS"
dpkg-deb -f "$OUT" Package Version Architecture Depends 2>/dev/null | sed 's/^/  /'
