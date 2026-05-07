"""Bearer-token middleware for the MCP HTTP transports.

The token is read from MCP_AUTH_TOKEN_PATH (the same file the Go bridge
writes via auth.go). We re-read the file on every request so a UI-triggered
regeneration takes effect immediately, without restarting the Python
process. The file is tiny (64 hex chars) so the per-request cost is
negligible compared to the network round-trip.

stdio is a subprocess transport — no network surface — so this module is
only loaded when MCP_TRANSPORT is sse or streamable-http.

The middleware is intentionally minimal:
    - 503 if the token file is missing (bridge has not finished booting yet)
    - 401 if the Authorization header is missing or malformed
    - 401 if the bearer value does not match (constant-time compare)
    - call_next otherwise

It deliberately does NOT cache the token, so token rotation is a write to a
file and requires no signal / restart on the Python side.
"""

import hmac
import os
from pathlib import Path

from starlette.middleware.base import BaseHTTPMiddleware
from starlette.responses import JSONResponse

DEFAULT_TOKEN_PATH = os.path.join(
    os.path.dirname(os.path.abspath(__file__)),
    "..",
    "whatsapp-bridge",
    "store",
    "auth_token",
)


def _token_path() -> str:
    """Path to the shared bearer-token file (env override wins)."""
    return os.getenv("MCP_AUTH_TOKEN_PATH", DEFAULT_TOKEN_PATH)


def _read_current_token() -> str | None:
    """Read the on-disk token, returning None if it is missing or empty."""
    try:
        value = Path(_token_path()).read_text(encoding="utf-8").strip()
    except (FileNotFoundError, OSError):
        return None
    return value or None


def _constant_time_eq(a: str, b: str) -> bool:
    """Length-safe constant-time string compare (uses bytes under the hood)."""
    return hmac.compare_digest(a.encode("utf-8"), b.encode("utf-8"))


class BearerAuthMiddleware(BaseHTTPMiddleware):
    """Reject every request that does not carry the current bearer token."""

    async def dispatch(self, request, call_next):
        expected = _read_current_token()
        if not expected:
            return JSONResponse(
                {"error": "auth token not initialized"},
                status_code=503,
            )

        header = request.headers.get("authorization", "")
        if not header.lower().startswith("bearer "):
            return JSONResponse(
                {"error": "missing bearer token"},
                status_code=401,
                headers={"WWW-Authenticate": 'Bearer realm="whatsapp-mcp"'},
            )

        provided = header.split(" ", 1)[1].strip()
        if not _constant_time_eq(provided, expected):
            return JSONResponse(
                {"error": "invalid bearer token"},
                status_code=401,
            )

        return await call_next(request)
