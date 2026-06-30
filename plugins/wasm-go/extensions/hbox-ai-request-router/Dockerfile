# 使用 scratch 作为基础镜像
FROM scratch

# 添加构建信息标签
ARG BUILD_TIME
ARG VERSION
LABEL build_time="${BUILD_TIME}"
LABEL version="${VERSION}"
LABEL maintainer="hbox-ai-request-router"

# 复制 wasm 文件，必须命名为 plugin.wasm
COPY main.wasm plugin.wasm
