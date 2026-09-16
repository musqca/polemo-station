APP_NAME = polemo-station
APP_PATH = .

# 编译路径
BUILD_PATH := ./dist

# 编译时间
BUILD_DATE = $(shell date +'%F %T')

GIT_BRANCH = -
GIT_COMMIT = -
GIT_REVISION = -

ifneq ($(wildcard .git),)
GIT_BRANCH = $(shell git rev-parse --abbrev-ref HEAD)
GIT_COMMIT = $(shell git rev-list --count HEAD)
GIT_REVISION = $(shell git rev-parse --short HEAD)
endif

# support包名称
FLAGS_PKG = github.com/wonli/aqi
LDFLAGS = "-X '$(FLAGS_PKG).BuildDate=$(BUILD_DATE)' \
		   -X '$(FLAGS_PKG).Branch=$(GIT_BRANCH)' \
		   -X '$(FLAGS_PKG).CommitVersion=$(GIT_COMMIT)' \
		   -X '$(FLAGS_PKG).Revision=$(GIT_REVISION)' \
		   -s -w"

# 编译参数
GO_FLAGS = -ldflags $(LDFLAGS) -trimpath -tags netgo

# Go编译命令 1-GOOS 2-GOARCH 3-FILE EXT
define go/build
	GOOS=$(1) GOARCH=$(2) CGO_ENABLED=0 go build $(GO_FLAGS) -o $(BUILD_PATH)/$(APP_NAME)-$(1)-$(2)-latest$(3) ${APP_PATH}
endef

# Generate binaries
.PHONY: build darwin linux windows pkg frontend

FRONTEND_DIR ?= ../polemo-station-web
FRONTEND_BUILD_DIR ?= $(FRONTEND_DIR)/build
EMBED_DIR := ./internal/frontend/build

DARWIN_ARCH ?= arm64
LINUX_ARCH ?= amd64
WINDOWS_ARCH ?= amd64
PKG_TARGETS ?= darwin linux windows

# Build the Station Web application and copy it into the Go package before
# compiling. VITE_STATION_* is intentionally not required: the embedded app
# uses same-origin /api and /ws URLs.
frontend:
	cd $(FRONTEND_DIR) && pnpm build:embedded
	rm -rf $(EMBED_DIR)
	mkdir -p $(EMBED_DIR)
	cp -R $(FRONTEND_BUILD_DIR)/. $(EMBED_DIR)/

# Build every release binary from the same embedded frontend. Each platform
# remains available as an individual target for CI matrix jobs.
pkg: $(PKG_TARGETS)

s1: linux
	@echo "Uploading to server.."
	scp -o ServerAliveInterval=15 -o ServerAliveCountMax=4 \
		$(BUILD_PATH)/polemo-station-linux-amd64-latest \
		s1:/data/pomelo/station/.polemo-station-linux-amd64-latest.next
	ssh -o ServerAliveInterval=15 -o ServerAliveCountMax=4 s1 'set -e; \
		cd /data/pomelo/station; \
		chmod 755 .polemo-station-linux-amd64-latest.next; \
		sudo rm -f polemo-station-linux-amd64-latest; \
		mv .polemo-station-linux-amd64-latest.next polemo-station-linux-amd64-latest; \
		sudo systemctl restart polemo_api.service'

darwin: frontend
	$(call go/build,darwin,$(DARWIN_ARCH))

linux: frontend
	$(call go/build,linux,$(LINUX_ARCH))

windows: frontend
	$(call go/build,windows,$(WINDOWS_ARCH),.exe)
