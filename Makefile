PAK_NAME := Delta Sync
PAK_DIR  := pak/$(PAK_NAME).pak
DIST     := dist
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# DROPBOX_APP_KEY bakes a shared Dropbox app key into the binary so users can
# just log in, exactly as Delta does with its own key. It is a public OAuth
# client ID (PKCE needs no secret); leave it empty and users supply their own.
DROPBOX_APP_KEY ?=

GO_LDFLAGS := -s -w -X main.version=$(VERSION) -X main.defaultAppKey=$(DROPBOX_APP_KEY)

.PHONY: all
all: test build

.PHONY: test
test:
	go vet ./...
	go test ./...
	sh test/launch_test.sh
	sh test/auto_sync_test.sh
	sh test/continue_state_ui_test.sh

.PHONY: fmt
fmt:
	gofmt -w .

# The TrimUI Brick / Smart Pro (tg5040) are arm64; older MinUI devices are
# 32-bit arm. Both are built so one .pak.zip covers every supported handheld.
.PHONY: build
build:
	mkdir -p "$(PAK_DIR)/bin/arm64" "$(PAK_DIR)/bin/arm"
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
		go build -trimpath -ldflags "$(GO_LDFLAGS)" -o "$(PAK_DIR)/bin/arm64/deltasync" ./cmd/deltasync
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
		go build -trimpath -ldflags "$(GO_LDFLAGS)" -o "$(PAK_DIR)/bin/arm/deltasync" ./cmd/deltasync
	chmod +x "$(PAK_DIR)/launch.sh" "$(PAK_DIR)/bin/shared/watch-and-sync" \
		"$(PAK_DIR)/bin/shared/pre-launch-hook.sh" \
		"$(PAK_DIR)/bin/shared/post-launch-hook.sh" \
		"$(PAK_DIR)/bin/shared/sync-worker"

# Desktop builds. Testing against a copy of the SD card on a computer is the
# fastest way to check the Delta side without touching the handheld.
.PHONY: desktop
desktop:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(DIST)/deltasync-linux-amd64   ./cmd/deltasync
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(DIST)/deltasync-macos-arm64   ./cmd/deltasync
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(DIST)/deltasync-macos-intel   ./cmd/deltasync
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(DIST)/deltasync-windows.exe  ./cmd/deltasync

.PHONY: package
package: build
	mkdir -p $(DIST)
	rm -f "$(DIST)/$(PAK_NAME).pak.zip"
	cd pak && zip -qr "../$(DIST)/$(PAK_NAME).pak.zip" "$(PAK_NAME).pak" \
		-x '*.DS_Store' -x '__MACOSX/*'
	@echo "Built $(DIST)/$(PAK_NAME).pak.zip"
	@echo "Copy it to /Tools/tg5040/ on the SD card and extract it in place."

.PHONY: clean
clean:
	rm -rf $(DIST)
	find "$(PAK_DIR)/bin" -mindepth 1 -maxdepth 1 ! -name shared -exec rm -rf {} +

# Fetches the on-screen menu helpers. These are published per DEVICE
# (minui-list-tg5040-nextui, ...), not per architecture. Without them the Pak
# can sync but cannot draw its menu, so CI treats them as required.
MINUI_PLATFORM ?= tg5040-nextui
MINUI_LIST_VERSION      ?= 0.15.0
MINUI_PRESENTER_VERSION ?= 0.13.0
MINUI_KEYBOARD_VERSION  ?= 0.9.0

.PHONY: deps
deps:
	@mkdir -p "$(PAK_DIR)/bin/$(firstword $(subst -, ,$(MINUI_PLATFORM)))"
	@set -e; dest="$(PAK_DIR)/bin/$(firstword $(subst -, ,$(MINUI_PLATFORM)))"; \
	for spec in "minui-list $(MINUI_LIST_VERSION)" \
	            "minui-presenter $(MINUI_PRESENTER_VERSION)" \
	            "minui-keyboard $(MINUI_KEYBOARD_VERSION)"; do \
	  name=$${spec%% *}; ver=$${spec##* }; \
	  url="https://github.com/josegonzalez/$$name/releases/download/$$ver/$$name-$(MINUI_PLATFORM)"; \
	  echo "fetching $$name-$(MINUI_PLATFORM)"; \
	  curl -fsSL -o "$$dest/$$name" "$$url" && chmod +x "$$dest/$$name"; \
	done
	@echo "Helpers in $(PAK_DIR)/bin/"
