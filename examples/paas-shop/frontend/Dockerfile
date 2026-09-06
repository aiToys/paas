# paas-shop frontend 镜像：node 构建 SPA -> nginx serve。
# build context 是 examples 目录：docker build -f examples/paas-shop/frontend/Dockerfile -t <img> examples/
FROM docker.m.daocloud.io/library/node:22-alpine AS builder
WORKDIR /app
COPY paas-shop/frontend/package.json ./
RUN npm config set registry https://registry.npmmirror.com && npm install
COPY paas-shop/frontend/ ./
RUN npm run build

FROM docker.m.daocloud.io/library/nginx:1.27-alpine
# 官方镜像启动脚本 envsubst 渲染 /etc/nginx/templates/*.template 到 conf.d：
# ${BFF_UPSTREAM} 按环境注入（prod 桶 appconfig 注入 -prod 上游），默认 test。
ENV BFF_UPSTREAM=paas-shop-bff:8080
COPY paas-shop/frontend/nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
