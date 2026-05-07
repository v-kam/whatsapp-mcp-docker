# stats.py — MCP tool-call statistics persistence.
#
# Provides:
#   - TOOL_REGISTRY: dict[name, description] populated at decoration time.
#   - @track_calls: decorator that increments call_count (or error_count on
#     exception) in the tool_stats SQLite table on every invocation.
#   - register_tools_in_db(): upserts the registry into the DB at startup.
#
# Concurrency: the writer (Python) uses WAL journal mode so the Go UI server
# can safely read the same file while we write. A short BUSY_TIMEOUT covers
# the rare contention window.

import functools
import os
import sqlite3
import threading
from datetime import datetime, timezone

DB_PATH = os.getenv(
    "MCP_STATS_DB_PATH",
    os.path.join(
        os.path.dirname(os.path.abspath(__file__)),
        "..",
        "whatsapp-bridge",
        "store",
        "mcp_stats.db",
    ),
)

# name -> first line of docstring (filled by @track_calls at import time)
TOOL_REGISTRY: dict[str, str] = {}

# A single shared connection is fine for sqlite3 in WAL mode as long as we
# guard writes with a lock. SQLite serialises writes anyway; the lock just
# avoids cursor contention from threaded MCP runtimes.
_lock = threading.Lock()
_conn: sqlite3.Connection | None = None


def _get_conn() -> sqlite3.Connection:
    """Lazy-open the SQLite connection, ensure schema, enable WAL."""
    global _conn
    if _conn is not None:
        return _conn

    os.makedirs(os.path.dirname(os.path.abspath(DB_PATH)), exist_ok=True)
    conn = sqlite3.connect(DB_PATH, timeout=5.0, check_same_thread=False)
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA busy_timeout=2000")
    conn.execute(
        """
        CREATE TABLE IF NOT EXISTS tool_stats (
            name            TEXT PRIMARY KEY,
            description     TEXT NOT NULL DEFAULT '',
            call_count      INTEGER NOT NULL DEFAULT 0,
            error_count     INTEGER NOT NULL DEFAULT 0,
            last_called_at  TIMESTAMP
        )
        """
    )
    conn.commit()
    _conn = conn
    return conn


def _record(name: str, success: bool) -> None:
    """Increment the appropriate counter and update last_called_at."""
    now = datetime.now(timezone.utc).isoformat(timespec="seconds")
    column = "call_count" if success else "error_count"
    try:
        with _lock:
            conn = _get_conn()
            conn.execute(
                f"""
                INSERT INTO tool_stats (name, {column}, last_called_at)
                VALUES (?, 1, ?)
                ON CONFLICT(name) DO UPDATE SET
                    {column} = {column} + 1,
                    last_called_at = excluded.last_called_at
                """,
                (name, now),
            )
            conn.commit()
    except sqlite3.Error:
        # Stats are best-effort; never fail the tool call because of a DB
        # hiccup. We swallow the error rather than logging to keep stdout
        # clean for the MCP stdio transport.
        pass


def track_calls(fn):
    """Decorator: record every call to fn in the tool_stats table.

    Apply this BELOW @mcp.tool() so FastMCP registers the wrapped function:

        @mcp.tool()
        @track_calls
        def my_tool(...): ...
    """
    name = fn.__name__
    doc = (fn.__doc__ or "").strip()
    description = doc.split("\n", 1)[0] if doc else ""
    TOOL_REGISTRY[name] = description

    @functools.wraps(fn)
    def wrapper(*args, **kwargs):
        try:
            result = fn(*args, **kwargs)
        except Exception:
            _record(name, success=False)
            raise
        _record(name, success=True)
        return result

    return wrapper


def register_tools_in_db() -> None:
    """Upsert TOOL_REGISTRY into tool_stats so all tools appear with 0 counts
    even before they have been called. Idempotent — never resets counters.
    """
    if not TOOL_REGISTRY:
        return
    try:
        with _lock:
            conn = _get_conn()
            for name, description in TOOL_REGISTRY.items():
                conn.execute(
                    """
                    INSERT INTO tool_stats (name, description)
                    VALUES (?, ?)
                    ON CONFLICT(name) DO UPDATE SET description = excluded.description
                    """,
                    (name, description),
                )
            conn.commit()
    except sqlite3.Error:
        pass
