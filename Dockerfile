# 构建阶段使用目标架构交叉编译；运行容器只包含 Agent 与 Docker 客户端。
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VCS_REF=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/CiBeDerORG/EasyHostAgent/internal/buildinfo.Commit=$VCS_REF" \
    -o /out/easyhostagent ./cmd/easyhostagent

# 官方 docker:cli 包含 docker compose 插件；不启动容器内 Docker daemon。
FROM docker:29-cli
RUN apk add --no-cache bash ca-certificates curl git openssh-client coreutils tar unzip
COPY --from=builder /out/easyhostagent /usr/local/bin/easyhostagent
ENV EASYHOSTAGENT_DATA_DIR=/var/lib/easyhostagent \
    EASYHOSTAGENT_SOFTWARE_ROOT=/software \
    EASYHOSTAGENT_LISTEN_ADDRESS=0.0.0.0:7018 \
    EASYHOSTAGENT_TRANSPORT=https \
    HOME=/tmp
EXPOSE 7018/tcp
USER root
ENTRYPOINT ["/usr/local/bin/easyhostagent"]
