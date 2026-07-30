import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ mode }) => {
  const apiTarget = loadEnv(mode, '.', '').TOOLHUB_VITE_API_TARGET ?? 'http://127.0.0.1:18480'
  return {
    plugins: [react()],
    server: {
      proxy: {
        '/api': apiTarget,
      },
    },
    build: { sourcemap: false, target: 'es2022' },
  }
})
