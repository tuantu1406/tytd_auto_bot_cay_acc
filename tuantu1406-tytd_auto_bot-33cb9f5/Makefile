.PHONY: help build build-native build-linux build-linux-amd64 build-linux-arm64 \
        build-windows build-windows-amd64 \
        build-darwin build-darwin-amd64 build-darwin-arm64 \
        build-gen test tidy clean proto run migrate build-encrypt

.DEFAULT_GOAL := help

GO ?= go
OS := $(shell $(GO) env GOOS)
ARCH := $(shell $(GO) env GOARCH)

# One config-driven binary: secrets (Telegram token, admin chat ID) come from
# config.yaml — see config.example.yaml, internal/appconfig. No more embedded
# keys, no more per-label builds.

# Per-platform build tags (darwin cannot link libc statically -> no "static")
TAGS_linux   := osusergo netgo static sqlcipher noantidebug
TAGS_windows := osusergo netgo static sqlcipher noantidebug
TAGS_darwin  := osusergo netgo sqlcipher noantidebug

LDFLAGS_STATIC := -s -w -extldflags '-fno-PIC -static'
LDFLAGS_PLAIN  := -s -w -extldflags '-static'

EXE_windows := .exe

# Cross-compilation toolchains (CGO + sqlcipher needs a real C compiler per
# target). Target == arch máy thì dùng gcc native (rỗng), không thì cần
# cross-compiler — override nếu toolchain của bạn tên khác:
#   make build CC_WINDOWS=... CC_LINUX_AMD64=... OSXCROSS=/path/to/osxcross
HOST_ARCH       := $(shell uname -m 2>/dev/null || echo unknown)
CC_LINUX_AMD64  ?= $(shell [ "$(HOST_ARCH)" = x86_64 ] || echo x86_64-linux-gnu-gcc)
CC_LINUX_ARM64  ?= $(shell [ "$(HOST_ARCH)" = aarch64 ] || echo aarch64-linux-gnu-gcc)
CC_WINDOWS      ?= x86_64-w64-mingw32-gcc
OSXCROSS        ?= /opt/osxcross
CC_DARWIN_AMD64 ?= $(OSXCROSS)/bin/o64-clang
CC_DARWIN_ARM64 ?= $(OSXCROSS)/bin/oa64-clang

# ---------------------------------------------------------------------------
# `make build` = MỘT phát ra đủ mọi arch (linux amd64+arm64, windows amd64,
# macOS amd64+arm64) -> build/. Target nào thiếu C toolchain cho cross-compile
# (sqlcipher = CGO) sẽ được skip kèm thông báo, các target còn vẫn build đủ.
# ---------------------------------------------------------------------------
build: build-linux-amd64 build-linux-arm64 build-windows-amd64 \
       build-darwin-amd64 build-darwin-arm64
	@echo ""
	@echo "==> Đủ arch trong build/:"
	@ls -1 build/ | grep '^main' | sed 's/^/    /'

# has_cc <compiler> — "yes" nếu compiler có sẵn (rỗng = gcc native của máy -> luôn yes)
has_cc = $(if $(1),$(shell command -v $(1) >/dev/null 2>&1 && echo yes),yes)
# recipe_skip <label> <compiler>
define recipe_skip
	@echo "[skip] $(1): thiếu C compiler '$(2)' — cài toolchain (xem 'make help') hoặc build trực tiếp trên đúng nền tảng đó"
endef

# Build chỉ cho máy hiện tại (host OS/arch) -> build/main[.exe]
build-native:
	@echo "Building bot for $(OS)/$(ARCH)..."
	mkdir -p build
	cd cmd && CGO_ENABLED=1 $(GO) build $(if $(filter linux,$(OS)),\
		-ldflags="$(LDFLAGS_STATIC)" -buildmode pie,\
		-ldflags="$(LDFLAGS_PLAIN)") \
		--trimpath -tags "$(TAGS_$(OS))" -o ../build/main$(EXE_$(OS)) main.go
	@echo "Build complete: build/main$(EXE_$(OS))"

# ---------------------------------------------------------------------------
# Từng target riêng lẻ
# ---------------------------------------------------------------------------
build-linux: build-linux-amd64 build-linux-arm64

ifeq ($(call has_cc,$(CC_LINUX_AMD64)),yes)
build-linux-amd64:
	@echo "Building bot for linux/amd64..."
	mkdir -p build
	cd cmd && CGO_ENABLED=1 GOOS=linux GOARCH=amd64 $(if $(CC_LINUX_AMD64),CC=$(CC_LINUX_AMD64) ,)$(GO) build \
		-ldflags="$(LDFLAGS_STATIC)" -buildmode pie --trimpath \
		-tags "$(TAGS_linux)" -o ../build/main-linux-amd64 main.go
	@echo "Build complete: build/main-linux-amd64"
else
build-linux-amd64:
	$(call recipe_skip,linux/amd64,$(CC_LINUX_AMD64))
endif

ifeq ($(call has_cc,$(CC_LINUX_ARM64)),yes)
build-linux-arm64:
	@echo "Building bot for linux/arm64..."
	mkdir -p build
	cd cmd && CGO_ENABLED=1 GOOS=linux GOARCH=arm64 $(if $(CC_LINUX_ARM64),CC=$(CC_LINUX_ARM64) ,)$(GO) build \
		-ldflags="$(LDFLAGS_STATIC)" -buildmode pie --trimpath \
		-tags "$(TAGS_linux)" -o ../build/main-linux-arm64 main.go
	@echo "Build complete: build/main-linux-arm64"
else
build-linux-arm64:
	$(call recipe_skip,linux/arm64,$(CC_LINUX_ARM64))
endif

build-windows: build-windows-amd64

ifeq ($(call has_cc,$(CC_WINDOWS)),yes)
build-windows-amd64:
	@echo "Building bot for windows/amd64..."
	mkdir -p build
	cd cmd && CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=$(CC_WINDOWS) $(GO) build \
		-ldflags="$(LDFLAGS_PLAIN)" --trimpath \
		-tags "$(TAGS_windows)" -o ../build/main-windows-amd64.exe main.go
	@echo "Build complete: build/main-windows-amd64.exe"
else
build-windows-amd64:
	$(call recipe_skip,windows/amd64,$(CC_WINDOWS))
endif

build-darwin: build-darwin-amd64 build-darwin-arm64

# macOS cross-builds need an osxcross SDK (macOS SDK + clang wrappers);
# thiếu thì build trực tiếp 'make build-native' trên máy Mac.
ifeq ($(call has_cc,$(CC_DARWIN_AMD64)),yes)
build-darwin-amd64:
	@echo "Building bot for darwin/amd64..."
	mkdir -p build
	cd cmd && CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CC=$(CC_DARWIN_AMD64) $(GO) build \
		-ldflags="$(LDFLAGS_PLAIN)" --trimpath \
		-tags "$(TAGS_darwin)" -o ../build/main-darwin-amd64 main.go
	@echo "Build complete: build/main-darwin-amd64 (unsigned: run 'codesign -s -' before use)"
else
build-darwin-amd64:
	$(call recipe_skip,darwin/amd64,$(CC_DARWIN_AMD64) [osxcross: OSXCROSS=<path>])
endif

ifeq ($(call has_cc,$(CC_DARWIN_ARM64)),yes)
build-darwin-arm64:
	@echo "Building bot for darwin/arm64..."
	mkdir -p build
	cd cmd && CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 CC=$(CC_DARWIN_ARM64) $(GO) build \
		-ldflags="$(LDFLAGS_PLAIN)" --trimpath \
		-tags "$(TAGS_darwin)" -o ../build/main-darwin-arm64 main.go
	@echo "Build complete: build/main-darwin-arm64 (unsigned: run 'codesign -s -' before use)"
else
build-darwin-arm64:
	$(call recipe_skip,darwin/arm64,$(CC_DARWIN_ARM64) [osxcross: OSXCROSS=<path>])
endif

# ---------------------------------------------------------------------------
# Tools & misc
# ---------------------------------------------------------------------------

# Token-encryption helper (cmd/gen): ./build/gen "<token>" -> telegram_token_encrypted
build-gen:
	@echo "Building gen tool..."
	mkdir -p build
	cd cmd && CGO_ENABLED=0 $(GO) build --trimpath -o ../build/gen gen.go
	@echo "Build complete: build/gen"

# Build the encryption migration tool
build-encrypt:
	@echo "Building encryption migration tool..."
	cd cmd && CGO_ENABLED=1 $(GO) build -tags sqlcipher -o encrypt_db_bin encrypt_db/main.go
	@echo "Build complete: cmd/encrypt_db_bin"

# Run the bot (native build; config.yaml is picked up from the current dir)
run: build-native
	@echo "Starting bot..."
	./build/main$(EXE_$(OS))

# Migrate data from plaintext to encrypted database
migrate: build-encrypt
	@echo "Running encryption migration..."
	cd cmd && ./encrypt_db_bin
	@echo "Migration complete"

clean:
	@echo "Cleaning build artifacts..."
	rm -rf ./build/*
	rm -f cmd/encrypt_db_bin cmd/encrypt_db_bin.exe
	@echo "Clean complete"

# Compile protobuf definitions (regenerate internal/proto/tytd.pb.go)
proto:
	@echo "Compiling proto..."
	cd internal/proto && PATH="/root/.asdf/installs/golang/1.22.2/bin:$$PATH" protoc --proto_path=. --go_out=. tytd.proto
	@echo "Proto compile complete: internal/proto/tytd.pb.go"

# Run tests
test:
	@echo "Running tests..."
	$(GO) test -tags sqlcipher ./...

# Tidy dependencies
tidy:
	@echo "Tidying dependencies..."
	$(GO) mod tidy

help:
	@echo "One binary, config-driven: put config.yaml next to it (config.example.yaml)."
	@echo ""
	@echo "Available targets:"
	@echo "  build             - MỘT phát ra đủ mọi arch (linux amd64+arm64, windows amd64,"
	@echo "                      macOS amd64+arm64) -> build/; thiếu toolchain nào thì skip target đó"
	@echo "  build-native      - chỉ máy hiện tại -> build/main[.exe]"
	@echo "  build-linux       - linux amd64+arm64 (arm64 cần $(CC_LINUX_ARM64))"
	@echo "  build-windows     - windows/amd64 (needs mingw-w64: $(CC_WINDOWS))"
	@echo "  build-darwin      - macOS amd64+arm64 (needs osxcross: $(OSXCROSS); or run 'make build-native' on a Mac)"
	@echo "  build-gen         - token-encryption helper for telegram_token_encrypted"
	@echo "  run               - Build (native) and run ./build/main with ./config.yaml"
	@echo "  build-encrypt     - Build the DB encryption migration tool"
	@echo "  migrate           - Run database encryption migration"
	@echo "  clean             - Remove build artifacts"
	@echo "  test              - Run tests"
	@echo "  tidy              - Tidy go.mod dependencies"
	@echo "  proto             - Regenerate protobuf code (tytd.pb.go)"
	@echo "  help              - Show this help message"
