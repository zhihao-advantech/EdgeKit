# third_party/webview_go

**Patched fork of [`github.com/webview/webview_go`](https://github.com/webview/webview_go)**
at `v0.0.0-20240831120633-6173450d4dd6` (upstream `master`), used via a `replace`
directive in the root `go.mod`.

## Why it is patched

Upstream hardcodes WebKitGTK **4.0** in its cgo pkg-config line:

```
#cgo linux ... pkg-config: gtk+-3.0 webkit2gtk-4.0
```

Ubuntu 24.04+ / Debian 13+ **removed** `webkit2gtk-4.0` and ship only
`webkit2gtk-4.1`, so an unpatched build does not compile there. This fork moves
the pkg-config name into two build-tag-guarded files:

| build | pkg-config name | distros |
| --- | --- | --- |
| default | `webkit2gtk-4.0` | Ubuntu 22.04, Debian 12 |
| `-tags webkit4_1` | `webkit2gtk-4.1` | Ubuntu 24.04+, Debian 13+ |

## Changes vs. upstream

1. `webview.go`: the linux pkg-config line was removed (moved to the shims).
2. `pkgconfig_webkit40.go` / `pkgconfig_webkit41.go`: the two tagged shims.
3. Windows-only `libs/mswebview2` headers and the blank imports for vendoring
   were dropped — EdgeKit targets Linux desktops only. `libs/webview/include/webview.h`
   is kept (the actual C++ header).

No Go or C++ source behaviour changed; only the linked WebKitGTK version can be
selected at build time.
