NAME := hbox-ai-request-router
VERSION := v1.1.5
BUILD_TIME := $(shell date +%Y%m%d-%H%M%S)

# 镜像仓库配置
IMAGE_REGISTRY ?= harbor.qihoo.net
IMAGE_NAMESPACE ?= intelligent-egineering-hbox2
IMAGE_REPO := $(IMAGE_NAMESPACE)/$(NAME)
IMAGE_TAG ?= $(VERSION)

FULL_IMAGE := $(IMAGE_REGISTRY)/$(IMAGE_REPO):$(IMAGE_TAG)
WASM_OUTPUT := main.wasm
DOCKERFILE := Dockerfile

# Go 编译参数（Go 1.24+，WASI）
GOOS := wasip1
GOARCH := wasm
GO_BUILD_FLAGS := -buildmode=c-shared -trimpath -ldflags="-s -w"

.PHONY: build build-image-multiarch tidy test clean

build:
	rm -f $(WASM_OUTPUT)
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(GO_BUILD_FLAGS) -o $(WASM_OUTPUT) ./

build-image-multiarch: build
	@echo "==> Building multi-architecture Docker image..."
	@echo "==> Target platforms: linux/amd64,linux/arm64"
	@docker buildx build \
		--builder desktop-linux \
		--platform linux/amd64,linux/arm64 \
		-f $(DOCKERFILE) \
		-t $(FULL_IMAGE) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		--build-arg VERSION=$(VERSION) \
		--push \
		. || (echo "ERROR: Multi-arch build failed" && exit 1)
	@echo "==> Multi-arch Docker image built and pushed: $(FULL_IMAGE)"

tidy:
	go mod tidy

test:
	go test ./...

clean:
	rm -f $(WASM_OUTPUT)
