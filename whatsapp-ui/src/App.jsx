// App.jsx — WhatsApp MCP web UI.
//
// Three tabs:
//   Status   — pairing state (waiting -> QR -> connected)
//   Connect  — MCP client config JSON, including the bearer token used to
//              authenticate against the HTTP transports. Supports copying
//              the JSON or just the token, plus rotating the token via
//              POST /api/ui/regenerate-token (which the Go bridge persists
//              to a file the Python middleware re-reads per request).
//   Tools    — list of MCP tools with call counters and error counters
//
// Each tab owns its own polling loop. Polling stops when the tab is hidden
// from the user (saves battery on mobile and reduces noise in container logs).

import { useEffect, useState, useCallback } from 'react'
import { QRCodeSVG } from 'qrcode.react'

const STATUS_POLL_MS = 2000
const TOOLS_POLL_MS = 5000

const TABS = [
  { id: 'status',  label: 'Status'  },
  { id: 'connect', label: 'Connect' },
  { id: 'tools',   label: 'Tools'   },
]

export default function App() {
  const [activeTab, setActiveTab] = useState('status')

  return (
    <div className="shell">
      <header className="header">
        <span className="logo">💬</span>
        <h1>WhatsApp MCP</h1>
      </header>

      <main className="card">
        <nav className="tabs" role="tablist">
          {TABS.map(t => (
            <button
              key={t.id}
              role="tab"
              aria-selected={activeTab === t.id}
              className={`tab ${activeTab === t.id ? 'tab--active' : ''}`}
              onClick={() => setActiveTab(t.id)}
            >
              {t.label}
            </button>
          ))}
        </nav>

        <div className="tab-content">
          {activeTab === 'status'  && <StatusTab />}
          {activeTab === 'connect' && <ConnectTab />}
          {activeTab === 'tools'   && <ToolsTab />}
        </div>
      </main>

      <footer className="footer">
        whatsapp-mcp · <a href="https://github.com/verygoodplugins/whatsapp-mcp" target="_blank" rel="noreferrer">GitHub</a>
      </footer>
    </div>
  )
}

// ── Status tab ─────────────────────────────────────────────────────────────

function StatusTab() {
  const [status, setStatus] = useState('waiting')
  const [qrCode, setQrCode] = useState('')
  const [error, setError] = useState(null)

  const fetchStatus = useCallback(async () => {
    try {
      const res = await fetch('/api/ui/status')
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data = await res.json()
      setStatus(data.status)
      setQrCode(data.qr_code ?? '')
      setError(null)
    } catch {
      setError('Cannot reach bridge — retrying...')
    }
  }, [])

  useEffect(() => {
    fetchStatus()
    if (status === 'connected') return
    const id = setInterval(fetchStatus, STATUS_POLL_MS)
    return () => clearInterval(id)
  }, [fetchStatus, status])

  return (
    <>
      {error && <p className="notice notice--error">{error}</p>}
      {status === 'waiting'   && <WaitingState />}
      {status === 'qr' && qrCode && <QRState qrCode={qrCode} />}
      {status === 'connected' && <ConnectedState />}
    </>
  )
}

function WaitingState() {
  return (
    <div className="state">
      <div className="spinner" aria-label="Loading" />
      <p className="state__title">Starting bridge&hellip;</p>
      <p className="state__sub">The QR code will appear here once the bridge is ready.</p>
    </div>
  )
}

function QRState({ qrCode }) {
  return (
    <div className="state">
      <p className="state__title">Scan with WhatsApp</p>
      <div className="qr-wrapper">
        <QRCodeSVG value={qrCode} size={240} level="L" includeMargin={true} />
      </div>
      <ol className="instructions">
        <li>Open WhatsApp on your phone</li>
        <li>Go to <strong>Settings → Linked Devices</strong></li>
        <li>Tap <strong>Link a Device</strong> and scan this code</li>
      </ol>
      <p className="state__sub muted">Code refreshes automatically every ~20 seconds.</p>
    </div>
  )
}

function ConnectedState() {
  return (
    <div className="state">
      <div className="check-icon" aria-hidden="true">✓</div>
      <p className="state__title">Device paired successfully!</p>
      <p className="state__sub">
        Your WhatsApp is connected. Use the <strong>Connect</strong> tab for the
        client configuration JSON.
      </p>
    </div>
  )
}

// ── Connect tab ────────────────────────────────────────────────────────────

function ConnectTab() {
  const [info, setInfo] = useState(null)
  const [error, setError] = useState(null)
  const [copiedKey, setCopiedKey] = useState(null) // 'json' | 'token' | null
  const [showToken, setShowToken] = useState(false)
  const [regenerating, setRegenerating] = useState(false)
  const [regenError, setRegenError] = useState(null)

  const loadInfo = useCallback(async () => {
    try {
      const res = await fetch('/api/ui/connection')
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      setInfo(await res.json())
      setError(null)
    } catch (err) {
      setError(err.message)
    }
  }, [])

  useEffect(() => { loadInfo() }, [loadInfo])

  const copyText = async (key, text) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopiedKey(key)
      setTimeout(() => setCopiedKey(null), 1500)
    } catch {
      // Clipboard API can fail on insecure origins; user can select manually.
    }
  }

  const regenerate = async () => {
    if (regenerating) return
    const ok = window.confirm(
      'Generate a new bearer token?\n\n' +
      'Existing MCP clients will start getting 401 errors until you copy the new ' +
      'token into their config and reconnect.'
    )
    if (!ok) return
    setRegenerating(true)
    setRegenError(null)
    try {
      const res = await fetch('/api/ui/regenerate-token', { method: 'POST' })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      await loadInfo()
      setShowToken(true)
    } catch (err) {
      setRegenError(err.message)
    } finally {
      setRegenerating(false)
    }
  }

  if (error) return <p className="notice notice--error">{error}</p>
  if (!info) return <p className="state__sub">Loading&hellip;</p>

  const configJSON = JSON.stringify(info.client_config, null, 2)
  const hasToken = Boolean(info.auth_token)

  return (
    <div className="connect">
      <div className="connect__header">
        <span className={`badge badge--${info.transport}`}>{info.transport.toUpperCase()}</span>
        {info.url && <code className="connect__url">{info.url}</code>}
      </div>

      {info.note && <p className="state__sub">{info.note}</p>}

      {hasToken && (
        <div className="token">
          <div className="token__label">
            <span>Bearer token</span>
            <span className="token__hint">required on every MCP request</span>
          </div>
          <div className="token__row">
            <code className="token__value">
              {showToken ? info.auth_token : maskToken(info.auth_token)}
            </code>
            <button
              type="button"
              className="token__btn"
              onClick={() => setShowToken(s => !s)}
              aria-pressed={showToken}
            >
              {showToken ? 'Hide' : 'Show'}
            </button>
            <button
              type="button"
              className="token__btn"
              onClick={() => copyText('token', info.auth_token)}
            >
              {copiedKey === 'token' ? 'Copied!' : 'Copy'}
            </button>
            <button
              type="button"
              className="token__btn token__btn--danger"
              onClick={regenerate}
              disabled={regenerating}
            >
              {regenerating ? 'Regenerating…' : 'Regenerate'}
            </button>
          </div>
          {regenError && <p className="notice notice--error">{regenError}</p>}
        </div>
      )}

      <div className="codeblock">
        <button className="codeblock__copy" onClick={() => copyText('json', configJSON)}>
          {copiedKey === 'json' ? 'Copied!' : 'Copy'}
        </button>
        <pre><code>{configJSON}</code></pre>
      </div>

      <p className="state__sub muted">
        Add this to <code>~/.cursor/mcp.json</code> (under <code>mcp.servers</code>) or
        <code> ~/Library/Application Support/Claude/claude_desktop_config.json</code>.
      </p>
    </div>
  )
}

// Returns a masked representation of the bearer token (first/last 4 chars
// visible, middle replaced with bullets). Used for the masked token view.
function maskToken(tok) {
  if (!tok) return ''
  if (tok.length <= 12) return '•'.repeat(tok.length)
  return `${tok.slice(0, 4)}${'•'.repeat(tok.length - 8)}${tok.slice(-4)}`
}

// ── Tools tab ──────────────────────────────────────────────────────────────

function ToolsTab() {
  const [tools, setTools] = useState([])
  const [error, setError] = useState(null)

  const fetchTools = useCallback(async () => {
    try {
      const res = await fetch('/api/ui/tools')
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      setTools(await res.json())
      setError(null)
    } catch (err) {
      setError(err.message)
    }
  }, [])

  useEffect(() => {
    fetchTools()
    const id = setInterval(fetchTools, TOOLS_POLL_MS)
    return () => clearInterval(id)
  }, [fetchTools])

  const totalCalls  = tools.reduce((n, t) => n + t.call_count,  0)
  const totalErrors = tools.reduce((n, t) => n + t.error_count, 0)

  if (error) return <p className="notice notice--error">{error}</p>

  if (tools.length === 0) {
    return (
      <p className="state__sub">
        No tools registered yet. The MCP server populates this list at startup
        once an AI client connects to it.
      </p>
    )
  }

  return (
    <div className="tools">
      <div className="tools__summary">
        <Metric label="Tools" value={tools.length} />
        <Metric label="Total calls" value={totalCalls} />
        <Metric label="Errors" value={totalErrors} variant={totalErrors > 0 ? 'warn' : null} />
      </div>

      <ul className="tools__list">
        {tools.map(t => (
          <li key={t.name} className="tool">
            <div className="tool__head">
              <code className="tool__name">{t.name}</code>
              <span className="tool__count">{t.call_count}</span>
            </div>
            {t.description && <p className="tool__desc">{t.description}</p>}
            <p className="tool__meta">
              {t.error_count > 0 && (
                <span className="tool__errors">{t.error_count} error{t.error_count === 1 ? '' : 's'}</span>
              )}
              {t.last_called_at && (
                <span className="tool__last">Last: {formatRelative(t.last_called_at)}</span>
              )}
            </p>
          </li>
        ))}
      </ul>
    </div>
  )
}

function Metric({ label, value, variant }) {
  return (
    <div className={`metric ${variant ? `metric--${variant}` : ''}`}>
      <div className="metric__value">{value}</div>
      <div className="metric__label">{label}</div>
    </div>
  )
}

// formatRelative converts an ISO timestamp into "5s ago", "3m ago", etc.
function formatRelative(iso) {
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return iso
  const sec = Math.max(0, Math.floor((Date.now() - ts) / 1000))
  if (sec < 60)    return `${sec}s ago`
  if (sec < 3600)  return `${Math.floor(sec / 60)}m ago`
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`
  return `${Math.floor(sec / 86400)}d ago`
}
