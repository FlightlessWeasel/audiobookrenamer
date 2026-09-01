# syntax=docker/dockerfile:1

# --- Stage 1: build the React frontend -------------------------------------
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build   # writes ../internal/webui/dist

# --- Stage 2: build the Go binary ----------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/audiobookrenamer ./cmd/audiobookrenamer

# --- Stage 3: runtime --------------------------------------------------------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget tzdata && \
    addgroup -S abr && adduser -S -G abr abr && \
    mkdir -p /config && chown abr:abr /config
COPY --from=build /out/audiobookrenamer /usr/local/bin/audiobookrenamer

USER abr
ENV ABR_CONFIG_DIR=/config \
    ABR_ADDR=:8674
EXPOSE 8674
VOLUME ["/config"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:8674/api/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/audiobookrenamer"]
