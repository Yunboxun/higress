NAME := am-identity
VERSION := v1.0.14
BUILD_TIME := $(shell date +%Y%m%d-%H%M%S)

# 镜像仓库配置
IMAGE_REGISTRY ?= harbor.qihoo.net
IMAGE_NAMESPACE ?= intelligent-egineering-hbox2
IMAGE_REPO := $(IMAGE_NAMESPACE)/$(NAME)
IMAGE_TAG ?= $(VERSION)

FULL_IMAGE := $(IMAGE_REGISTRY)/$(IMAGE_REPO):$(IMAGE_TAG)
WASM_OUTPUT := main.wasm
DOCKERFILE := Dockerfile

# Go编译参数 (Go 1.24+)
GOOS := wasip1
GOARCH := wasm
GO_VERSION := 1.24.0
GO_BUILD_FLAGS := -buildmode=c-shared -trimpath -ldflags="-s -w"

build:
	rm -f main.wasm
	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o main.wasm ./

build-image-multiarch: build
	@echo "$(BLUE)==> Building multi-architecture Docker image...$(NC)"
	@echo "$(YELLOW)==> Target platforms: linux/amd64,linux/arm64$(NC)"
	@docker buildx inspect $(NAME)-builder > /dev/null 2>&1 || \
		docker buildx create --name $(NAME)-builder --use
	@docker buildx build \
		--platform linux/amd64,linux/arm64 \
		-f $(DOCKERFILE) \
		-t $(FULL_IMAGE) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		--build-arg VERSION=$(VERSION) \
		--push \
		. || (echo "$(RED)ERROR: Multi-arch build failed$(NC)" && exit 1)
	@echo "$(GREEN)==> Multi-arch Docker image built and pushed: $(FULL_IMAGE)$(NC)"