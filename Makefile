APP  := edgekit
BIN  := build/$(APP)
PKG  := ./cmd/$(APP)

GO          ?= go
GOTOOLCHAIN ?= local
export GOTOOLCHAIN

# WebKitGTK on some distributions is linked against a newer libstdc++ than the
# one the default g++ search path picks up, which makes the final link fail with
# "undefined reference to ...@GLIBCXX_3.4.30". Pointing the linker at the
# runtime libstdc++ first resolves it without touching system files.
SHIM              := build/.libstdcxx
SYSTEM_LIBSTDCXX  := $(shell g++ -print-file-name=libstdc++.so.6)
CGO_LDFLAGS       ?= -L$(SHIM)
export CGO_LDFLAGS

.PHONY: all build run headless tidy vet fmt clean shim

all: build

$(SHIM)/libstdc++.so:
	@mkdir -p $(SHIM)
	ln -sf $(SYSTEM_LIBSTDCXX) $(SHIM)/libstdc++.so

shim: $(SHIM)/libstdc++.so

build: shim
	$(GO) build -o $(BIN) $(PKG)

run: shim
	$(GO) run $(PKG)

headless: shim
	$(GO) run $(PKG) -headless

tidy:
	$(GO) mod tidy

vet: shim
	$(GO) vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf build
