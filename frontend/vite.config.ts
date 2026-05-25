import fs from 'node:fs'
import path from 'node:path'

import react from '@vitejs/plugin-react'
import { defineConfig, loadEnv } from 'vite'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const devHttpsEnabled = env.VITE_DEV_HTTPS === 'true'
  const backendProtocol = env.VITE_BACKEND_PROTOCOL || 'http'
  const backendHost = env.VITE_BACKEND_HOST || 'localhost'
  const backendPort = env.VITE_BACKEND_PORT || '8080'

  let https
  if (devHttpsEnabled) {
    const certFile = env.VITE_DEV_TLS_CERT_FILE
    const keyFile = env.VITE_DEV_TLS_KEY_FILE

    if (!certFile || !keyFile) {
      throw new Error(
        'VITE_DEV_TLS_CERT_FILE and VITE_DEV_TLS_KEY_FILE must be set when VITE_DEV_HTTPS=true',
      )
    }

    https = {
      // Reuse the mkcert-generated certificate so the browser trusts local HTTPS.
      cert: fs.readFileSync(path.resolve(certFile)),
      key: fs.readFileSync(path.resolve(keyFile)),
    }
  }

  return {
    plugins: [react()],
    server: {
      port: Number(env.VITE_DEV_PORT || 5173),
      host: true,
      https,
      proxy: {
        '/ws': {
          // Keep the browser on the same HTTPS origin while proxying WebSocket traffic to the backend.
          target: `${backendProtocol}://${backendHost}:${backendPort}`,
          ws: true,
          secure: false,
        },
      },
    },
  }
})
