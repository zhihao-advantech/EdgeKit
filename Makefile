APP  := edgekit
BIN  := build/$(APP)
PKG  := ./cmd/$(APP)

GO          ?= go
GOTOOLCHAIN ?= local
export GOTOOLCHAIN

# Version reported by the binary and used by the packages. Prefers the nearest
# git tag; falls back to 0.1.0 when the tree has no tags yet.
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo 0.1.0)

# WebKitGTK variant. Ubuntu 22.04 / Debian 12 ship 4.0, Ubuntu 24.04+ / Debian 13+
# ship only 4.1, so the package must be built per target. Auto-detected from
# pkg-config; override with `make WEBKIT=4.1 ...`.
WEBKIT ?= $(shell pkg-config --exists webkit2gtk-4.1 && echo 4.1 || echo 4.0)
ifeq ($(WEBKIT),4.1)
BUILD_TAGS := webkit4_1
else
BUILD_TAGS :=
endif

GO_TAGS  := $(if $(BUILD_TAGS),-tags $(BUILD_TAGS),)
GO_LDFLAGS := -X main.appVersion=$(VERSION)

# WebKitGTK on some distributions is linked against a newer libstdc++ than the
# one the default g++ search path picks up, which makes the final link fail with
# "undefined reference to ...@GLIBCXX_3.4.30". Pointing the linker at the
# runtime libstdc++ first resolves it without touching system files.
SHIM              := build/.libstdcxx
SYSTEM_LIBSTDCXX  := $(shell g++ -print-file-name=libstdc++.so.6)
CGO_LDFLAGS       ?= -L$(SHIM)
export CGO_LDFLAGS

# Packaging configuration (used by `make package-*`).
PKGDIR ?= packaging
DIST   ?= dist

.PHONY: all build run headless vet tidy fmt clean shim info package package-deb package-run

all: build

$(SHIM)/libstdc++.so:
	@mkdir -p $(SHIM)
	ln -sf $(SYSTEM_LIBSTDCXX) $(SHIM)/libstdc++.so

shim: $(SHIM)/libstdc++.so

build: shim
	$(GO) build $(GO_TAGS) -ldflags "$(GO_LDFLAGS)" -o $(BIN) $(PKG)

run: shim
	$(GO) run $(GO_TAGS) -ldflags "$(GO_LDFLAGS)" $(PKG)

headless: shim
	$(GO) run $(GO_TAGS) -ldflags "$(GO_LDFLAGS)" $(PKG) -headless

vet: shim
	$(GO) vet $(GO_TAGS) ./...

tidy:
	$(GO) mod tidy

fmt:
	gofmt -w .

clean:
	rm -rf build $(DIST)

# Print what will be built, for CI logs.
info:
	@echo "version: $(VERSION)"
	@echo "webkit:  $(WEBKIT) (tags: $(BUILD_TAGS))"
	@echo "bin:     $(BIN)"

# Debian package (declares system dependencies and prints a verified table).
package-deb: build
	VERSION="$(VERSION)" WEBKIT="$(WEBKIT)" DIST="$(DIST)" $(PKGDIR)/make-deb.sh

# Self-extracting installer (checks dependencies itself, supports --uninstall).
package-run: build
	VERSION="$(VERSION)" WEBKIT="$(WEBKIT)" DIST="$(DIST)" $(PKGDIR)/make-run.sh

# Both package formats for the current build variant.
package: package-deb package-run
