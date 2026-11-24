GO ?= go
BIN := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GITCOMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X github.com/maxtanforge/moori/internal/version.Version=$(VERSION) \
	-X github.com/maxtanforge/moori/internal/version.GitCommit=$(GITCOMMIT)
# Both current binaries use pure Go; the control UI is embedded from source.
PLATFORMS := linux/amd64,linux/arm64
IMG_SERVER ?= moori
IMG_AGENT ?= moori-agent
PREFIX ?= /usr/local

.PHONY: build test test-browser vet fmt clean install image-server image-control image-agent release-images release-agent

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/moori ./cmd/moori
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/moori-agent ./cmd/moori-agent

install: build
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 0755 $(BIN)/moori $(BIN)/moori-agent $(DESTDIR)$(PREFIX)/bin/

test:
	$(GO) test ./...

test-browser:
	@test -x "$$MOORI_BROWSER_BIN" || (echo 'Set MOORI_BROWSER_BIN to the Chromium executable' >&2; exit 1)
	$(GO) test ./internal/control/web -run Browser -count=1 -v

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -rf $(BIN)

# Server 镜像（含嵌入界面）：多阶段构建见 deploy/Dockerfile，
# 构建上下文 = 仓库根目录。单平台产物可用 --platform linux/amd64 --load 验证。
image-server:
	docker buildx build -f deploy/Dockerfile --platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) --build-arg GITCOMMIT=$(GITCOMMIT) \
		-t $(IMG_SERVER):$(VERSION) .

# 仅管理远程 Agent 的主服务镜像，省略 Docker CLI 和 Compose。
image-control:
	docker buildx build -f deploy/Dockerfile --target control --platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) --build-arg GITCOMMIT=$(GITCOMMIT) \
		-t $(IMG_SERVER):$(VERSION)-slim .

# Agent 镜像（含 Docker CLI + Compose 插件）：deploy/Dockerfile.agent。
image-agent:
	docker buildx build -f deploy/Dockerfile.agent --platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) --build-arg GITCOMMIT=$(GITCOMMIT) \
		-t $(IMG_AGENT):$(VERSION) .

# 同版本构建 moori 和 moori-agent 镜像。
release-images: image-server image-agent

# Agent 独立二进制（systemd 方式）：双架构 tar.gz + SHA256SUMS + sizes.txt。
# 不需要 docker，任意装 Go 的环境可跑。
release-agent:
	bash scripts/release-agent.sh $(VERSION)
