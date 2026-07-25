import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:18480',
      '/agent': { target: 'ws://127.0.0.1:18480', ws: true },
    },
  },
  build: { sourcemap: false, target: 'es2022' },
})
