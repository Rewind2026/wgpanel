import { existsSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 前端产物直接输出到 Go 的 embed 目录，保证后端嵌入的就是最新构建。
const embedOutDir = path.resolve(__dirname, '../internal/web/dist')

// go:embed 不允许匹配到空目录，全新克隆后若不先构建前端，
// `go build` 会直接失败。这里在构建结束后补写占位文件作为兜底。
const placeholder = path.join(embedOutDir, '.gitkeep')

export default defineConfig({
  plugins: [
    react(),
    {
      // 保证 embed 目录始终非空，使 Go 工程在未构建前端时也能编译。
      name: 'wgpanel-embed-placeholder',
      closeBundle() {
        if (!existsSync(placeholder)) writeFileSync(placeholder, '')
      },
    },
  ],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  build: {
    outDir: embedOutDir,
    // 清空目录时会删掉 .gitkeep，故在 closeBundle 中重新补写。
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        // 把体积大且更新频率低的依赖拆出去，改善首屏加载。
        //
        // Vite 8 底层换成 Rolldown，Rollup 的 manualChunks 已废弃，
        // 改用 advancedChunks.groups（语义更接近 webpack 的 splitChunks）。
        advancedChunks: {
          groups: [
            {
              name: 'react',
              test: /node_modules[\\/](react|react-dom|react-router-dom)[\\/]/,
            },
            {
              name: 'query',
              test: /node_modules[\\/]@tanstack[\\/]react-query[\\/]/,
            },
          ],
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: true },
      '/healthz': { target: 'http://127.0.0.1:8080', changeOrigin: true },
      '/readyz': { target: 'http://127.0.0.1:8080', changeOrigin: true },
    },
  },
})