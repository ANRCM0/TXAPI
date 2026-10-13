# syntax=docker/dockerfile:1

# ---- 阶段 1：编译 ----
#
# 为什么用多阶段：编译期需要完整的 Go 工具链（几百 MB）和源码里的
# go.sum，而运行期只需要一个静态二进制。不分阶段的话镜像里既有
# 工具链又有源码，体积大 5 倍以上，而且源码（含历史里的密钥风险）
# 会留在最终镜像的层里——「镜像里没有任何密钥」这条要求只有在
# 丢弃编译阶段后才真正成立。
#
# 为什么 golang:1.23-alpine 而不是 golang:1.23-bookworm：
#   - 项目是纯 Go（依赖里有 modernc.org/sqlite 这种纯 Go 实现，
#     不需要 cgo），alpine 的 musl libc 足够；
#   - alpine 基础镜像 7MB vs debian 70MB+，最终镜像差距明显。
#   - CGO_ENABLED=0 显式关掉 cgo：既是「纯静态」的保证（产物可以
#     跑在 scratch/distroless 上），也避免 musl 下 cgo 的偶发问题。
FROM golang:1.23-alpine AS builder

WORKDIR /src

# 先拷依赖清单再拷源码：Docker 的层缓存是按「这一层之前有没有变」
# 判断的。依赖清单不变时 `go mod download` 这一层直接命中缓存，
# 改一行业务代码不会触发重新下载全部依赖——本地构建从 2 分钟
# 降到几秒。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# -trimpath 去掉构建机上的绝对路径：同一个提交在任何机器上编出的
# 二进制逐字节一致，方便校验和比对（也顺带不把构建机的目录结构
# 泄进二进制）。
# -ldflags="-s -w" 去掉符号表和 DWARF 调试信息：自用网关不需要
# 远程调试，体积能小 25% 左右。
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/gateway .

# ---- 阶段 2：运行 ----
#
# 为什么 alpine 而不是 scratch：SQLite 需要能创建/写入数据卷上的
# 文件，scratch 连 shell 和基本文件工具都没有，排障时进不去容器；
# alpine 有 ash 和 ls，足够「进去看看库文件在不在、有多大」。
FROM alpine:3.20

# ca-certificates：上游 base_url 是 https 时必须（OpenAI/Anthropic
# 官方端点全是 TLS）。少了它所有 HTTPS 转发都会证书校验失败。
# tzdata：日志时间戳和 SQLite 的 created_at 按本地时区格式化，
# 没有时区库会全部退化成 UTC，和宿主机对不上。
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# 非 root 运行：网关不需要 root 权限（监听 8080 以上、写 /app/data），
# 而 root 容器一旦被 RCE 就是整机失守。这是最低成本的安全加固。
#
# 数据目录在建用户之后创建并 chown：alpine 的 adduser 不建家目录
# 之外的路径，而 /app/data 必须在进程启动前就属于 app 用户——
# 否则 SQLite 建库文件会因权限失败。
RUN adduser -D -u 10001 app \
    && mkdir -p /app/data \
    && chown -R app:app /app

COPY --from=builder --chown=app:app /out/gateway /app/gateway

# 配置文件**不**进镜像：渠道的上游 Key 是凭据，写进 Dockerfile 或
# 提交进仓库等于把密钥公开。部署时用 volume 挂载一份 config.yaml，
# Key 通过 GATEWAY_CHANNEL_KEY_<id> 环境变量注入（见 config.Load
# 的 applyChannelKeyEnv）——见 docker-compose.yml。
#
# 这里放一份模板只是为了 `docker run` 不带挂载时能看到完整示例，
# 它里面的 key 全是占位符，不含任何真实凭据。
COPY --chown=app:app config.yaml /app/config.yaml

USER app

# 声明数据卷：SQLite 文件落在 /app/data。声明出来是为了让
# `docker run` 默认就有一个匿名卷（数据不会被容器删除带走），
# compose 里会显式命名卷。
VOLUME ["/app/data"]

# 不设 EXPOSE：监听地址由 config.yaml 的 server.addr 决定（可绑
# 127.0.0.1 也可 0.0.0.0），写死端口会和配置不一致造成误导。
# compose 里按配置的端口映射即可。

# healthcheck 用 /healthz：它免鉴权、免限流（见 server.go 的注释），
# 且不依赖数据库和任何上游——网关进程活着就能答。用 wget 而不是
# curl：alpine 默认不带 curl，装它是为了这一个请求不值得。
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/gateway"]
CMD ["-config", "/app/config.yaml"]
