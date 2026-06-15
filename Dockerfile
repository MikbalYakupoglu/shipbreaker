# ── Stage 1: Build UI ────────────────────────────────────────────────────────
FROM node:22-alpine AS ui-builder

WORKDIR /app/ui
COPY ui/package*.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY ui/ ./
RUN npm run build

# ── Stage 2: Build Go binary ──────────────────────────────────────────────────
FROM golang:1.25-alpine AS go-builder

WORKDIR /app

# Download dependencies first (layer cache)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Copy source
COPY . .

# Copy built UI into the embed path
COPY --from=ui-builder /app/ui/dist ./internal/api/dist

# Build — CGO disabled, static binary
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /breaker ./cmd/breaker

# ── Stage 3: Minimal runtime image ───────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=go-builder /breaker /usr/local/bin/breaker

# Default data directory (mount a volume here for persistence)
RUN mkdir -p /data

EXPOSE 7777

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:7777/healthz || exit 1

ENTRYPOINT ["breaker", "serve"]
