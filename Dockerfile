# Platform Core 容器镜像（多阶段构建，前端嵌入单镜像）。
#
# 阶段：
#   1. frontend：node 构建 console-user/console-admin/landing 三套 SPA，产物按 base path 子路径化
#   2. builder：golang 编译静态二进制，前端产物 embed 进 internal/web/dist
#   3. runtime：distroless 无 shell 最小攻击面，非 root，暴露 :8080
#
# 同域路由（core 单镜像 serve 前端 + API，无 CORS）：
#   /api/* /v1/* /openapi.json /docs /livez → API
#   /console/* → console-user   /admin/* → console-admin   /* → landing

# runtime 基础镜像（全局 ARG，供第 3 阶段 FROM；legacy builder 需 ARG 在首个 FROM 前）。
# 国内默认 alpine:3.20（gcr distroless 不在 daocloud 白名单拉不到）；海外可
# --build-arg RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot 回 distroless（需同步去 apk add/USER 行）。
ARG RUNTIME_IMAGE=docker.m.daocloud.io/alpine:3.20

# 基础镜像 registry（全局 ARG）：国内默认走 daocloud 中转，避免 docker.io/gcr 直拉超时。
# 海外构建可 --build-arg BASE_REGISTRY=docker.io 回官方源。
ARG BASE_REGISTRY=docker.m.daocloud.io

# ---------- 1. 前端构建阶段 ----------
# node:22-alpine：console-admin preinstall 要求 Node >= 22.13.0。
FROM ${BASE_REGISTRY}/node:22-alpine AS frontend
# 国内 npm 镜像（外网受限环境必需）；海外构建可 --build-arg NPM_REGISTRY=https://registry.npmjs.org 覆盖。
ARG NPM_REGISTRY=https://registry.npmmirror.com
WORKDIR /fe
RUN corepack enable && corepack prepare pnpm@9.12.3 --activate
RUN pnpm config set registry "$NPM_REGISTRY"
# 先拷 lockfile + workspace 清单，利用层缓存加速 install
COPY frontend/package.json frontend/pnpm-workspace.yaml frontend/pnpm-lock.yaml ./
COPY frontend/console-user/package.json ./console-user/package.json
COPY frontend/console-admin/package.json ./console-admin/package.json
COPY frontend/landing/package.json ./landing/package.json
RUN pnpm install --frozen-lockfile
# 拷源码（含已配置的 base path）
COPY frontend/ ./
# 三套构建：用目录路径 filter（与 frontend/package.json 一致；console-admin 包名非 console-admin）。
# console-admin 传 VITE_BASE='/admin/'（子路径）；console-user 已在 vite.config 固定 '/console/'；landing 默认 '/'
RUN pnpm --filter ./console-user build && \
    VITE_BASE='/admin/' pnpm --filter ./console-admin build && \
    pnpm --filter ./landing build

# ---------- 2. Go 构建阶段 ----------
# builder 跑本地架构（$BUILDPLATFORM，如 arm64 Mac）避 QEMU 全栈模拟——Go http2/TLS 在
# QEMU amd64 模拟下必 SIGSEGV（fault in http2Framer.ReadFrame），go mod download 直接 crash。
# 用 buildkit（DOCKER_BUILDKIT=1）+ --platform=$BUILDPLATFORM 让 builder 跑 host 架构，
# Go 经 GOARCH=amd64 交叉编译到目标架构（runtime 仍 linux/amd64）。需 buildkit 内置 frontend
# （不加 # syntax= 指令，不拉远程 frontend）。
FROM --platform=$BUILDPLATFORM ${BASE_REGISTRY}/golang:1.26-alpine AS builder
# 国内 Go 代理（外网受限环境必需）；海外构建可 --build-arg GOPROXY=https://proxy.golang.org,direct 覆盖。
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 前端产物覆盖 internal/web/dist 占位（embed 编译期吸收）
COPY --from=frontend /fe/console-user/dist ./internal/web/dist/console-user
COPY --from=frontend /fe/console-admin/dist ./internal/web/dist/console-admin
COPY --from=frontend /fe/landing/dist ./internal/web/dist/landing
# 静态编译（CGO 禁用，适配 distroless）。
# GOARCH=amd64：builder 跑在本地（如 arm64）用 Go 交叉编译到 amd64（多数 K8s 集群架构），
# 避免 QEMU 全栈模拟；arm64 集群可 --build-arg GOARCH=arm64 覆盖。
ARG GOARCH=amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${GOARCH} go build -trimpath -ldflags="-s -w" -o /out/core ./cmd/core

# ---------- 3. 运行阶段 ----------
# alpine runtime（Go 静态二进制 CGO_ENABLED=0 不依赖 glibc/musl，alpine 可跑）。
# 装 ca-certificates 供 HTTPS（airouter 网关 / OpenAI/DeepSeek 等供应商 API）。
# RUNTINE_IMAGE 用全局 ARG 默认（daocloud alpine:3.20）；海外可
# --build-arg RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot 回 distroless
# （需同步去掉 apk add/USER 行，distroless 内置 nonroot）。
FROM ${RUNTIME_IMAGE}
# apk 切国内镜像源（dl-cdn.alpinelinux.org 在国内网络常超时）；海外构建可去掉此 sed。
RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories && \
    apk add --no-cache ca-certificates && update-ca-certificates
WORKDIR /
COPY --from=builder /out/core /core
# 非 root 运行（最小权限，与 distroless nonroot 同 uid=65532）
RUN adduser -D -u 65532 nonroot
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/core"]
