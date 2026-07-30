# syntax=docker/dockerfile:1
# Platform Core 容器镜像（多阶段构建）。
# 构建期：golang 编译静态二进制；运行期：distroless 无 shell 最小攻击面。
# 非root 运行；暴露 :8080（OpenAI 兼容 + 平台 REST + 探针）。

# ---------- 构建阶段 ----------
FROM golang:1.25-alpine AS builder
WORKDIR /src

# 先拷依赖清单，利用层缓存加速
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 静态编译（CGO 禁用，适配 distroless）
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/core ./cmd/core

# ---------- 运行阶段 ----------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=builder /out/core /core
# nonroot 用户（distroless 镜像内置 uid=65532）
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/core"]
