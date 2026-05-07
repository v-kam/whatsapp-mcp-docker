// vite.config.js — build configuration for the WhatsApp pairing UI.
//
// In production (Docker): `npm run build` outputs to dist/, which the
//   Dockerfile copies to /app/whatsapp-bridge/ui/ where the Go server serves it.
// In development: `npm run dev` proxies /api/* to the Go UI server so you can
//   develop the UI without rebuilding the container.

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api/ui': 'http://localhost:3000',
    },
  },
})
