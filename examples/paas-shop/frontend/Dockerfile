# paas-shop frontend 镜像：node 构建 SPA -> nginx serve。
# build context 是 examples 目录：docker build -f examples/paas-shop/frontend/Dockerfile -t <img> examples/
FROM docker.m.daocloud.io/library/node:22-alpine AS builder
WORKDIR /app
COPY paas-shop/frontend/package.json ./
RUN npm config set registry https://registry.npmmirror.com && npm install
COPY paas-shop/frontend/ ./
RUN npm run build

FROM docker.m.daocloud.io/library/nginx:1.27-alpine
COPY paas-shop/frontend/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
