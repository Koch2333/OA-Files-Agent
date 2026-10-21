import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// 开发时把 /v1、/healthz、/readyz 代理到 Go 后端，避免 CORS；后端地址可由 OA_API_TARGET 覆盖。
const target = process.env.OA_API_TARGET ?? 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: {
    port: 5173,
    proxy: {
      '/v1': { target, changeOrigin: false },
      '/healthz': { target },
      '/readyz': { target },
    },
  },
  test: { environment: 'node', include: ['src/**/*.test.ts'] },
})
