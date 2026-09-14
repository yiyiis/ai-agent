# 前端镜像：build 产物 + 一个只管发静态文件的 Caddy。
#
# 它**不做** TLS、不做 /api 路由——那是宿主机上那份 Caddy 的事。这里只监听 :80，
# 由宿主机 Caddy 反代进来。做成镜像是为了回滚：换个 tag 重启即可，
# 不用重新 build。
#
# 国内构建换源：
#   docker build -f frontend/Dockerfile frontend \
#     --build-arg NPM_REGISTRY=https://registry.npmmirror.com -t ai-agent-web:dev

# node 版本有下限：vite 8 的 engines 是 ^20.19.0 || >=22.12.0，
# node:20-alpine（当前 20.18.x）装不上，别随手降。
ARG NODE_IMAGE=node:22-alpine
ARG CADDY_IMAGE=caddy:2-alpine

FROM ${NODE_IMAGE} AS builder

ARG NPM_REGISTRY=https://registry.npmmirror.com

WORKDIR /src

# 先只拷 lock 文件，依赖没变就复用这层
COPY package.json package-lock.json ./
RUN npm config set registry "$NPM_REGISTRY" && npm ci

COPY . .
# npm run build = tsc -b && vite build，类型错误会让构建失败——这是故意的，
# 让类型问题挡在发版之前而不是发版之后
RUN npm run build

FROM ${CADDY_IMAGE}

COPY --from=builder /src/dist /srv
COPY Caddyfile /etc/caddy/Caddyfile

EXPOSE 80

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1/ || exit 1
