//go:build !webkit4_1

// EdgeKit vendor note: this tag selects the WebKitGTK 4.0 pkg-config name,
// which is what Ubuntu 22.04 / Debian 12 and older provide. Build with
// `-tags webkit4_1` for Ubuntu 24.04+ / Debian 13+, which ship only 4.1.
package webview

// #cgo linux openbsd freebsd netbsd pkg-config: gtk+-3.0 webkit2gtk-4.0
import "C"
