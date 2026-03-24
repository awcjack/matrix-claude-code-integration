# Matrix-Claude Code Integration Monorepo
#
# Packages:
#   - bot: Single-session bot mode (no session isolation)
#   - appservice: Multi-session appservice mode (session isolation via IPC)

.PHONY: all build build-bot build-appservice clean test lint help

# Default target
all: build

# Build all packages
build: build-bot build-appservice

# ============================================================================
# BOT PACKAGE (Single Session)
# ============================================================================

build-bot:
	@echo "Building bot package..."
	cd packages/bot && go build -o ../../bin/matrix-claude-bot ./cmd

run-bot: build-bot
	./bin/matrix-claude-bot --config config.bot-mode.example.json

# ============================================================================
# APPSERVICE PACKAGE (Multi Session)
# ============================================================================

build-appservice: build-coordinator build-bridge

build-coordinator:
	@echo "Building coordinator..."
	cd packages/appservice && go build -o ../../bin/matrix-coordinator ./cmd

build-bridge:
	@echo "Building bridge..."
	cd packages/appservice && go build -o ../../bin/matrix-bridge ./cmd/bridge

run-coordinator: build-appservice
	./bin/matrix-coordinator -config config.coordinator.example.json

# ============================================================================
# COMMON TARGETS
# ============================================================================

clean:
	@echo "Cleaning..."
	rm -rf bin/
	rm -f /tmp/matrix-claude.sock

test:
	cd packages/bot && go test -v ./...
	cd packages/appservice && go test -v ./...
	cd packages/shared && go test -v ./...

lint:
	cd packages/bot && golangci-lint run ./...
	cd packages/appservice && golangci-lint run ./...

tidy:
	cd packages/bot && go mod tidy
	cd packages/appservice && go mod tidy
	cd packages/shared && go mod tidy

# ============================================================================
# DOCKER
# ============================================================================

docker-build-bot:
	docker build -f packages/bot/Dockerfile -t matrix-claude-bot:latest .

docker-build-appservice:
	docker build -f packages/appservice/Dockerfile -t matrix-claude-appservice:latest .

# ============================================================================
# HELP
# ============================================================================

help:
	@echo "Matrix-Claude Code Integration Monorepo"
	@echo ""
	@echo "Packages:"
	@echo "  bot        - Single-session bot mode (simpler, no isolation)"
	@echo "  appservice - Multi-session mode (isolated sessions via IPC)"
	@echo ""
	@echo "Build Targets:"
	@echo "  build              - Build all packages"
	@echo "  build-bot          - Build bot package only"
	@echo "  build-appservice   - Build appservice (coordinator + bridge)"
	@echo "  build-coordinator  - Build coordinator only"
	@echo "  build-bridge       - Build bridge only"
	@echo ""
	@echo "Run Targets:"
	@echo "  run-bot            - Run bot in dev mode"
	@echo "  run-coordinator    - Run coordinator in dev mode"
	@echo ""
	@echo "Other:"
	@echo "  clean              - Remove build artifacts"
	@echo "  test               - Run all tests"
	@echo "  lint               - Run linter"
	@echo "  tidy               - Run go mod tidy on all packages"
	@echo "  help               - Show this help"
