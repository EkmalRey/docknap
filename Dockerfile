FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags="-s -w -X main.version=0.1.0" -o /out/docknap .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/docknap /usr/local/bin/docknap
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/docknap"]
LABEL org.opencontainers.image.title="docknap"
LABEL org.opencontainers.image.description="A lazy-loading reverse proxy for Docker containers"
LABEL org.opencontainers.image.source="https://github.com/ekmalrey/docknap"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.vendor="ekmalrey"
