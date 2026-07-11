FROM --platform=$BUILDPLATFORM golang:1.25-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/docknap .

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
RUN apk add --no-cache ca-certificates tzdata wget \
 && addgroup -S docknap && adduser -S -G docknap -h /nonexistent -s /sbin/nologin docknap
COPY --from=builder /out/docknap /usr/local/bin/docknap
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8000/healthz >/dev/null 2>&1 || exit 1
USER docknap
ENTRYPOINT ["/usr/local/bin/docknap"]
LABEL org.opencontainers.image.title="docknap"
LABEL org.opencontainers.image.description="A lazy-loading reverse proxy for Docker containers"
LABEL org.opencontainers.image.source="https://github.com/ekmalrey/docknap"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.vendor="ekmalrey"
