FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=1.0.0
ARG COMMIT=none
ARG BUILD_AT=unknown
RUN mkdir -p /out/app \
    && CGO_ENABLED=0 go build -trimpath \
        -ldflags "-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_AT}" \
        -o /out/demo-service . \
    && cp config.yaml application.yaml nacos.yaml /out/

FROM gcr.io/distroless/static-debian12:nonroot
# COPY creates /app before WORKDIR, so --chown also owns the directory. Sync
# replaces config.yaml atomically and the pid file is written into app/, so a
# non-root process needs both to be writable.
# 让 COPY 先于 WORKDIR 创建 /app，--chown 才能同时作用于目录。Sync 会原子替换
# config.yaml，pid 文件写在 app/ 下，非 root 进程要求两者都可写。
COPY --from=build --chown=65532:65532 /out/ /app/
WORKDIR /app
# A container is restarted by Kubernetes or the Docker restart policy, so a
# config change exits instead of reloading in place. The strategy is an
# environment override on purpose: it wins over application.yaml in the image.
# 容器由 Kubernetes 或 Docker restart policy 拉起，因此配置变更时退出而非原地
# 热更新。策略刻意用环境变量注入：它优先于镜像里的 application.yaml。
ENV NACOS_CONFIG_CHANGE_STRATEGY=exit
EXPOSE 8080
ENTRYPOINT ["/app/demo-service"]
