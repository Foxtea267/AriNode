# Build go
FROM golang:1.25.0-alpine AS builder
WORKDIR /app
COPY . .
ENV CGO_ENABLED=0
ARG VERSION=dev
RUN GOEXPERIMENT=jsonv2 go mod download
RUN GOEXPERIMENT=jsonv2 go build -v -o arinode -tags "sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor" -ldflags "-X github.com/Foxtea267/AriNode/cmd.version=${VERSION}" . \
    && GOEXPERIMENT=jsonv2 go build -o anctl -tags "sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor" -ldflags "-X github.com/Foxtea267/AriNode/cmd.version=${VERSION}" ./cmd/anctl

# Release
FROM  alpine
# 安装必要的工具包
RUN  apk --update --no-cache add tzdata ca-certificates \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime
RUN mkdir /etc/arinode/
COPY --from=builder /app/arinode /usr/local/bin
COPY --from=builder /app/anctl /usr/local/bin

ENTRYPOINT [ "arinode", "server", "--config", "/etc/arinode/config.json"]
