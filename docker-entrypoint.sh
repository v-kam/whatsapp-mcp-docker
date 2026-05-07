#!/bin/sh
# docker-entrypoint.sh — container startup script for whatsapp-mcp.
#
# Default mode (docker run -p 3000:3000 ... whatsapp-mcp):
#   Starts the Go bridge in the background, waits until the bridge process is
#   up (detected via the UI server on WHATSAPP_UI_PORT), then starts the MCP
#   server. Open http://localhost:3000 to scan the QR code if this is a fresh
#   device. MCP tools work once pairing is complete and the bridge REST API
#   comes up automatically.
#
# pair mode (docker run -it ... whatsapp-mcp pair):
#   Runs only the Go bridge in the foreground for terminal QR output.
#   Any extra arguments after "pair" are forwarded to the bridge binary.

BRIDGE_DIR="/app/whatsapp-bridge"
BRIDGE_BIN="${BRIDGE_DIR}/whatsapp-bridge"
MCP_DIR="/app/whatsapp-mcp-server"
UI_PORT="${WHATSAPP_UI_PORT:-3000}"

# The bridge resolves "store/" and "ui/" relative to its working directory,
# so we must run it from /app/whatsapp-bridge so SQLite lands in the volume
# at /app/whatsapp-bridge/store/ and the UI server finds the React build.
cd "$BRIDGE_DIR" || exit 1

# ── pair mode ────────────────────────────────────────────────────────────────
if [ "$1" = "pair" ]; then
    echo "[entrypoint] pair mode — starting bridge; scan the QR code with WhatsApp"
    shift
    exec "$BRIDGE_BIN" "$@"
fi

# ── default mode ─────────────────────────────────────────────────────────────
echo "[entrypoint] starting Go bridge ..."
"$BRIDGE_BIN" &
BRIDGE_PID=$!

# Wait up to 30 seconds for the bridge process to be ready.
# We probe the UI server (port ${UI_PORT}) which starts immediately on bridge
# launch — long before pairing. The REST API (:8080) only starts after pairing,
# so we must NOT wait for it here.
echo "[entrypoint] waiting for bridge to start (checking http://127.0.0.1:${UI_PORT}/api/ui/status) ..."
RETRIES=30
while [ "$RETRIES" -gt 0 ]; do
    if curl -sf "http://127.0.0.1:${UI_PORT}/api/ui/status" > /dev/null 2>&1; then
        break
    fi
    RETRIES=$((RETRIES - 1))
    if [ "$RETRIES" -eq 0 ]; then
        echo "[entrypoint] bridge did not start in time — check logs above" >&2
        kill "$BRIDGE_PID" 2>/dev/null || true
        exit 1
    fi
    sleep 1
done

echo "[entrypoint] bridge is up — open http://localhost:${UI_PORT} to pair if needed"
echo "[entrypoint] starting MCP server (transport=${MCP_TRANSPORT:-stdio})"

# exec replaces this shell so SIGTERM/SIGINT are forwarded to uv → main.py
exec uv --directory "$MCP_DIR" run main.py
