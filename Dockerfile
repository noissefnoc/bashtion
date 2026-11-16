# Stage 1: Build static bashtion binary using Go 1.27
FROM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS builder

ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/noissefnoc/bashtion/internal/bashtion.Version=${VERSION}" -o /bin/bashtion ./cmd/bashtion

# Stage 2: Clean Linux testing environment (Ubuntu 26.04 LTS)
FROM ubuntu:26.04@sha256:065aa8626841f16057b78b027b464cd59c4fac36c204c72dcbf48d66f5fa8a96

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
