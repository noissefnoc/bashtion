# Stage 1: Build static bashtion binary using Go 1.27
FROM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS builder

ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/noissefnoc/bashtion/internal/bashtion.Version=${VERSION}" -o /bin/bashtion ./cmd/bashtion

# Stage 2: Clean Linux testing environment (Ubuntu 26.04 LTS)
FROM ubuntu:26.04@sha256:d66b366dac61b575bee2aef07e9c26e8070096fbe84a3d4d3eb0da069b5d76db

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    bats \
    git \
    coreutils \
    sed \
    grep \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Run as non-root user for container security
RUN if ! id -u 1000 >/dev/null 2>&1; then \
        useradd -u 1000 -m -s /bin/bash bashtion; \
    else \
        existing_user=$(getent passwd 1000 | cut -d: -f1); \
        usermod -l bashtion -d /home/bashtion -m -s /bin/bash "$existing_user"; \
        groupmod -n bashtion "$existing_user" || true; \
    fi && \
    mkdir -p /workspace && \
    chown -R bashtion:bashtion /workspace

COPY --from=builder /bin/bashtion /usr/local/bin/bashtion

USER bashtion
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/bashtion"]
CMD ["--help"]
