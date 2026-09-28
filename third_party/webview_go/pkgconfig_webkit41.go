//go:build webkit4_1

// EdgeKit vendor note: this tag selects the WebKitGTK 4.1 pkg-config name,
// which is what Ubuntu 24.04+ / Debian 13+ provide (they no longer ship 4.0).
// The default build (no tag) targets WebKitGTK 4.0.
package webview

// #cgo linux openbsd freebsd netbsd pkg-config: gtk+-3.0 webkit2gtk-4.1
import "C"
