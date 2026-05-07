// ui.go — pairing web UI server for the WhatsApp bridge.
//
// Exposes a tiny HTTP server (default :3000) on 0.0.0.0 that:
//   - serves the pre-built React static files from staticDir
//   - GET  /api/ui/status            → pairing state
//   - GET  /api/ui/connection        → MCP client config (incl. bearer token for HTTP modes)
//   - POST /api/ui/regenerate-token  → rotate the bearer token, return the new value
//   - GET  /api/ui/tools             → tool list + call counters from store/mcp_stats.db
//
// State is written by main.go (pairing.Update) and read here; a sync.RWMutex
// keeps concurrent access safe. The main REST API (127.0.0.1:8080) is
// unaffected — this server is a completely separate listener.
//
// The bearer token is shared with the Python MCP server through a small file
// (see auth.go). The UI never restarts the MCP process; the Python middleware
// re-reads the token file on every request.

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
)

// PairingStatus holds the current QR / connection state shared between the
// WhatsApp QR loop and the web UI HTTP handler. All fields are protected by mu.
type PairingStatus struct {
	mu     sync.RWMutex
	Status string // "waiting" | "qr" | "connected"
	QRCode string // raw QR string; empty unless Status == "qr"
}

// pairing is the global instance updated by main.go.
var pairing = &PairingStatus{Status: "waiting"}

// Update sets the pairing state atomically. Safe for concurrent use.
func (s *PairingStatus) Update(status, qrCode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = status
	s.QRCode = qrCode
}

// Get returns a consistent snapshot of the current pairing state.
// Safe for concurrent use.
func (s *PairingStatus) Get() (status, qrCode string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status, s.QRCode
}

// getEnvInt returns the integer value of an env var, or defaultVal when the
// variable is unset or cannot be parsed.
func getEnvInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

// StartUIServer starts the pairing web UI in a background goroutine.
// staticDir should point to the pre-built React output directory.
// When staticDir does not exist the status API still works; static files
// return 404 (acceptable during development / before the first build).
//
// As a side effect, the bearer token file is created on first launch so the
// Python MCP middleware (which races against this server during container
// startup) always has something to read.
func StartUIServer(port int, staticDir string) {
	if _, err := EnsureToken(); err != nil {
		fmt.Printf("WARN: failed to initialize auth token: %v\n", err)
	}

	mux := http.NewServeMux()

	// /api/ui/status — returns current pairing state as JSON.
	// CORS header allows the Vite dev server (different port) to call this.
	mux.HandleFunc("/api/ui/status", func(w http.ResponseWriter, r *http.Request) {
		status, qrCode := pairing.Get()
		writeJSON(w, map[string]string{
			"status":  status,
			"qr_code": qrCode,
		})
	})

	// /api/ui/connection — returns the MCP client config the user needs.
	mux.HandleFunc("/api/ui/connection", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, buildConnectionPayload())
	})

	// /api/ui/regenerate-token — rotates the bearer token and returns the new value.
	// The new token is written to the shared token file; the Python middleware
	// picks it up on the next request, so existing connections that still send
	// the old token start receiving 401.
	mux.HandleFunc("/api/ui/regenerate-token", func(w http.ResponseWriter, r *http.Request) {
		// CORS preflight for the Vite dev server.
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		tok, err := RegenerateToken()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"auth_token": tok})
	})

	// /api/ui/tools — returns the tool registry + call counters.
	mux.HandleFunc("/api/ui/tools", func(w http.ResponseWriter, r *http.Request) {
		tools, err := readToolStats()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, tools)
	})

	// Static files — serve the React build. Unknown paths fall back to
	// index.html so client-side routing works correctly.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := staticDir + r.URL.Path
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// Serve index.html for unmatched paths (SPA fallback)
			http.ServeFile(w, r, staticDir+"/index.html")
			return
		}
		http.FileServer(http.Dir(staticDir)).ServeHTTP(w, r)
	})

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	fmt.Printf("Pairing UI available at http://localhost:%d\n", port)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			fmt.Printf("UI server error: %v\n", err)
		}
	}()
}

// writeJSON writes v as JSON with a permissive CORS header for the Vite dev
// server. Errors are silently swallowed — the response has already started.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(v)
}

// ConnectionPayload is the JSON shape returned by /api/ui/connection.
//
// AuthToken is only populated for HTTP transports; stdio runs as a
// subprocess and has no network surface to protect.
type ConnectionPayload struct {
	Transport    string `json:"transport"`
	URL          string `json:"url,omitempty"`
	Note         string `json:"note,omitempty"`
	AuthToken    string `json:"auth_token,omitempty"`
	ClientConfig any    `json:"client_config"`
}

// buildConnectionPayload constructs the connection details from MCP_TRANSPORT
// / MCP_PORT environment variables. The container only knows the in-container
// port; we assume the user maps the same port to the host (which the README
// recommends). For HTTP transports the bearer token is loaded (or generated
// on first call) and embedded in the client_config.headers so users can copy
// it straight into their MCP client config.
func buildConnectionPayload() ConnectionPayload {
	transport := os.Getenv("MCP_TRANSPORT")
	if transport == "" {
		transport = "stdio"
	}

	if transport == "stdio" {
		return ConnectionPayload{
			Transport: "stdio",
			Note:      "Spawned as a subprocess by Cursor / Claude Desktop. Add this to your MCP config:",
			ClientConfig: map[string]any{
				"mcpServers": map[string]any{
					"whatsapp": map[string]any{
						"command": "docker",
						"args": []string{
							"run", "-i", "--rm",
							"-p", "3000:3000",
							"-v", "whatsapp-data:/app/whatsapp-bridge/store",
							"-e", "FORWARD_SELF=false",
							"whatsapp-mcp",
						},
					},
				},
			},
		}
	}

	mcpPort := getEnvInt("MCP_PORT", 8000)
	path := "/sse"
	if transport == "streamable-http" {
		path = "/mcp"
	}
	url := fmt.Sprintf("http://localhost:%d%s", mcpPort, path)

	tok, err := EnsureToken()
	if err != nil {
		fmt.Printf("WARN: could not load/generate auth token: %v\n", err)
	}

	return ConnectionPayload{
		Transport: transport,
		URL:       url,
		AuthToken: tok,
		Note: fmt.Sprintf(
			"HTTP mode. Make sure you started the container with -p %d:%d so the host can reach the MCP server. The Authorization header below is required on every request.",
			mcpPort, mcpPort,
		),
		ClientConfig: map[string]any{
			"mcpServers": map[string]any{
				"whatsapp": map[string]any{
					"url":       url,
					"transport": transport,
					"headers": map[string]any{
						"Authorization": "Bearer " + tok,
					},
				},
			},
		},
	}
}

// ToolStat is one row returned by /api/ui/tools.
type ToolStat struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	CallCount    int64  `json:"call_count"`
	ErrorCount   int64  `json:"error_count"`
	LastCalledAt string `json:"last_called_at,omitempty"`
}

// readToolStats opens mcp_stats.db read-only and returns all rows.
// If the DB does not yet exist (e.g. the MCP server has not started) it
// returns an empty slice rather than an error.
func readToolStats() ([]ToolStat, error) {
	dbPath := os.Getenv("MCP_STATS_DB_PATH")
	if dbPath == "" {
		dbPath = "store/mcp_stats.db"
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return []ToolStat{}, nil
	}

	// _journal_mode=wal so we play nicely with the Python writer.
	dsn := fmt.Sprintf("file:%s?mode=ro&_journal_mode=wal", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`
		SELECT name, description, call_count, error_count,
		       COALESCE(last_called_at, '') AS last_called_at
		FROM tool_stats
		ORDER BY call_count DESC, name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]ToolStat, 0)
	for rows.Next() {
		var t ToolStat
		if err := rows.Scan(&t.Name, &t.Description, &t.CallCount, &t.ErrorCount, &t.LastCalledAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
