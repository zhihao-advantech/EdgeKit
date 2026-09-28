#!/bin/sh
# EdgeKit runtime dependency check.
#
# Verifies that the system packages EdgeKit links against are installed at a
# sufficient version. Exits non-zero when something is missing or too old.
#
# The WebKitGTK variant is read from EDGEKIT_WEBKIT (4.0 or 4.1); when unset it
# falls back to /usr/lib/edgekit/variant, then to the copy next to this script,
# then to 4.0. This one script is shared by the .deb postinst and the .run
# installer.
#
# A requirement may list alternative package names separated by '|' (Ubuntu
# 24.04 renamed libgtk-3-0 to libgtk-3-0t64 for the 64-bit time_t transition,
# keeping the old name only as a virtual Provides).
set -u

WEBKIT="${EDGEKIT_WEBKIT:-}"
if [ -z "$WEBKIT" ] && [ -r /usr/lib/edgekit/variant ]; then
	WEBKIT=$(cat /usr/lib/edgekit/variant)
fi
if [ -z "$WEBKIT" ]; then
	here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
	[ -r "$here/variant" ] && WEBKIT=$(cat "$here/variant")
fi
case "$WEBKIT" in
4.1)
	WEBKIT_LIB="libwebkit2gtk-4.1"
	REQUIRED="libgtk-3-0t64|libgtk-3-0:3.24 libwebkit2gtk-4.1-0:2.42 libstdc++6:12"
	;;
*)
	WEBKIT="4.0"
	WEBKIT_LIB="libwebkit2gtk-4.0"
	REQUIRED="libgtk-3-0|libgtk-3-0t64:3.24 libwebkit2gtk-4.0-37:2.36 libstdc++6:12"
	;;
esac

missing=0
echo "EdgeKit 依赖检查（WebKitGTK $WEBKIT）："

if command -v dpkg-query >/dev/null 2>&1; then
	printf '  %-28s %-8s %s\n' "软件包" "状态" "已装版本"
	for spec in $REQUIRED; do
		alts=${spec%%:*}
		min=${spec#*:}
		got=""
		used=""
		saveifs=$IFS
		IFS='|'
		for cand in $alts; do
			v=$(dpkg-query -W -f='${Version}' "$cand" 2>/dev/null || true)
			if [ -n "$v" ]; then
				got=$v
				used=$cand
				break
			fi
		done
		IFS=$saveifs
		if [ -z "$got" ]; then
			label=$(printf '%s' "$alts" | tr '|' '/')
			printf '  %-28s %-8s %s\n' "$label" "缺失" "需要 >= $min"
			missing=1
		elif dpkg --compare-versions "$got" ge "$min" 2>/dev/null; then
			printf '  %-28s %-8s %s\n' "$used" "满足" "$got"
		else
			printf '  %-28s %-8s %s\n' "$used" "过旧" "$got（需要 >= $min）"
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
	# First alternative of each requirement, for the apt hint.
	pkgs=""
	for spec in $REQUIRED; do
		pkgs="$pkgs $(printf '%s' "${spec%%:*}" | cut -d'|' -f1)"
	done
	echo
	echo "  缺少或过旧的依赖会导致 EdgeKit 无法启动。"
	echo "  Ubuntu / Debian 可执行："
	echo "    sudo apt update && sudo apt install -y$pkgs"
	exit 1
fi

echo "  依赖满足。"
exit 0
