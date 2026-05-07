# Multi-stage Dockerfile for whatsapp-mcp.
#
# Stage 1 (bridge-builder): compiles the Go bridge binary using CGO (required
#   by go-sqlite3).
# Stage 2 (ui-builder):     builds the React pairing UI with Vite/Node.
# Stage 3 (runtime):        slim Python image with uv; combines Go binary,
#   React static files, and Python MCP server into one container.
#
# Ports:
#   3000 — pairing web UI (open in browser to scan QR code; WHATSAPP_UI_PORT)
#   8000 — MCP HTTP/SSE transport (MCP_TRANSPORT=sse; MCP_PORT)
#   8080 — Go bridge REST API (internal only, loopback)
#
# MCP transport modes (MCP_TRANSPORT env var):
#   stdio            — default; AI client spawns container with docker run -i
#   sse              — HTTP SSE server on MCP_PORT (default 8000)
#   streamable-http  — newer streamable-HTTP MCP protocol on MCP_PORT

# ── Stage 1: build Go bridge ────────────────────────────────────────────────
FROM golang:1.24-alpine AS bridge-builder

# gcc + musl-dev are required for CGO (go-sqlite3 uses cgo)
RUN apk add --no-cache gcc musl-dev

WORKDIR /build

COPY whatsapp-bridge/go.mod whatsapp-bridge/go.sum ./
RUN go mod download

COPY whatsapp-bridge/ ./
# Build a fully static binary so it runs on the glibc-based python:3.12-slim
# runtime. -linkmode external -extldflags '-static' links musl + sqlite3 into
# the binary, removing any shared-library dependency.
RUN CGO_ENABLED=1 GOOS=linux go build \
    -ldflags="-linkmode external -extldflags '-static'" \
    -o whatsapp-bridge .

# ── Stage 2: build React pairing UI ─────────────────────────────────────────
FROM node:20-alpine AS ui-builder

WORKDIR /ui

# Install deps first for better layer caching
COPY whatsapp-ui/package.json whatsapp-ui/package-lock.json ./
RUN npm ci

# Build the React app; output goes to dist/
COPY whatsapp-ui/ ./
RUN npm run build

# ── Stage 3: Python runtime ──────────────────────────────────────────────────
FROM python:3.12-slim

# Install runtime C library needed by the Go-compiled sqlite3 binary,
# plus curl (used in the entrypoint bridge-ready poll) and ffmpeg (optional
# audio conversion for send_audio_message).
RUN apt-get update && apt-get install -y --no-install-recommends \
        curl \
        ffmpeg \
    && rm -rf /var/lib/apt/lists/*

# Install uv for fast, reproducible Python dependency installation
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

WORKDIR /app

# Copy Go binary
COPY --from=bridge-builder /build/whatsapp-bridge /app/whatsapp-bridge/whatsapp-bridge

# Copy pre-built React UI (served by the Go bridge at :3000)
COPY --from=ui-builder /ui/dist /app/whatsapp-bridge/ui/

# Copy Python source and lockfile; install deps from the frozen lockfile
COPY whatsapp-mcp-server/ /app/whatsapp-mcp-server/
RUN uv sync --frozen --no-dev --project /app/whatsapp-mcp-server

# Copy entrypoint
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

# SQLite store — mount a named volume here to persist session + message data
VOLUME /app/whatsapp-bridge/store

# 3000 — pairing web UI (always available)
# 8000 — MCP HTTP/SSE transport (only relevant when MCP_TRANSPORT != stdio)
EXPOSE 3000
EXPOSE 8000

ENTRYPOINT ["/app/docker-entrypoint.sh"]
