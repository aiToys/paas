# 平台示例镜像（mcp-server + traffic-gen）。
# 本地预编译 linux/amd64 二进制后，单阶段 alpine COPY（避免容器内 go mod download 网络问题）。
#
# 示例是独立 Go module（github.com/aitoys/paas-examples），构建须在 examples/ 目录：
#   cd examples
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/mcp-server ./mcp-server
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/traffic-gen ./traffic-gen
#   docker build -f examples/Dockerfile -t <nodeIP>:30050/paas/paas-examples:v1 /tmp
#   docker push <nodeIP>:30050/paas/paas-examples:v1
# （context 是 /tmp，含两个预编译二进制；alpine 经 daocloud 中转）

ARG BASE_REGISTRY=docker.m.daocloud.io
FROM ${BASE_REGISTRY}/alpine:3.20
RUN apk add --no-cache ca-certificates
COPY mcp-server /usr/local/bin/mcp-server
COPY traffic-gen /usr/local/bin/traffic-gen
# 默认跑 traffic-gen 常驻模式；CronJob/Deployment 用 command 覆盖指定二进制
ENTRYPOINT ["traffic-gen"]
