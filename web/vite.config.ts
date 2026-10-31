import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    // 开发时只代理 API；发布后前端与 API 同源，由 Go 直接提供。
    proxy: {
      '/api': 'http://127.0.0.1:8080',
    },
  },
})
