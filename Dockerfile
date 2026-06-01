FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags="-s -w -X main.version=0.1.3" -o /out/docknap .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=builder /out/docknap /usr/local/bin/docknap
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8000/_docknap/status >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/docknap"]
LABEL org.opencontainers.image.title="docknap"
LABEL org.opencontainers.image.description="A lazy-loading reverse proxy for Docker containers"
LABEL org.opencontainers.image.source="https://github.com/ekmalrey/docknap"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.vendor="ekmalrey"
