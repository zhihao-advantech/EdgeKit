#!/bin/sh
# EdgeKit runtime dependency check.
#
# Verifies that the system packages EdgeKit links against are installed at a
# sufficient version. Exits non-zero when something is missing or too old.
#
# The WebKitGTK variant is read from EDGEKIT_WEBKIT (4.0 or 4.1); when unset it
# falls back to /usr/lib/edgekit/variant, then to 4.0. This one script is shared
# by the .deb postinst and the .run installer.
set -u

WEBKIT="${EDGEKIT_WEBKIT:-}"
if [ -z "$WEBKIT" ] && [ -r /usr/lib/edgekit/variant ]; then
	WEBKIT=$(cat /usr/lib/edgekit/variant)
fi
case "$WEBKIT" in
4.1)
	WEBKIT_LIB="libwebkit2gtk-4.1"
	REQUIRED="libgtk-3-0:3.24 libwebkit2gtk-4.1-0:2.42 libstdc++6:12"
	;;
*)
	WEBKIT="4.0"
	WEBKIT_LIB="libwebkit2gtk-4.0"
	REQUIRED="libgtk-3-0:3.24 libwebkit2gtk-4.0-37:2.36 libstdc++6:12"
	;;
esac

# Version a package must have at least; checked with dpkg when available.
missing=0
echo "EdgeKit 依赖检查（WebKitGTK $WEBKIT）："

if command -v dpkg-query >/dev/null 2>&1; then
	printf '  %-28s %-8s %s\n' "软件包" "状态" "已装版本"
	for spec in $REQUIRED; do
		pkg=${spec%%:*}
		min=${spec#*:}
		got=$(dpkg-query -W -f='${Version}' "$pkg" 2>/dev/null || true)
		if [ -z "$got" ]; then
			printf '  %-28s %-8s %s\n' "$pkg" "缺失" "需要 >= $min"
			missing=1
		elif dpkg --compare-versions "$got" ge "$min" 2>/dev/null; then
			printf '  %-28s %-8s %s\n' "$pkg" "满足" "$got"
		else
			printf '  %-28s %-8s %s\n' "$pkg" "过旧" "$got（需要 >= $min）"
			missing=1
		fi
	done
else
	# Non-Debian systems: fall back to checking the shared libraries.
	echo "  （未找到 dpkg，改为检查共享库）"
	for lib in libgtk-3.so.0 "$WEBKIT_LIB.so.0" libstdc++.so.6; do
		if ldconfig -p 2>/dev/null | grep -q "$lib"; then
			printf '  %-28s %-8s\n' "$lib" "满足"
		else
			printf '  %-28s %-8s\n' "$lib" "缺失"
			missing=1
		fi
	done
fi

if [ "$missing" -ne 0 ]; then
	echo
	echo "  缺少或过旧的依赖会导致 EdgeKit 无法启动。"
	echo "  Ubuntu / Debian 可执行："
	if [ "$WEBKIT" = "4.1" ]; then
		echo "    sudo apt update && sudo apt install -y libgtk-3-0 libwebkit2gtk-4.1-0 libstdc++6"
	else
		echo "    sudo apt update && sudo apt install -y libgtk-3-0 libwebkit2gtk-4.0-37 libstdc++6"
	fi
	exit 1
fi

echo "  依赖满足。"
exit 0
