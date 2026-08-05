import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  plugins: [svelte()],
  // Wails 从应用根目录提供嵌入资源。
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'esnext',
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
    alias: [
      {
        // 测试必须无法触达真实 Wails 桥，避免启动付费或联网任务。
        find: /^.*\/wailsjs\/go\/ui\/App$/,
        replacement: fileURLToPath(
          new URL('./src/lib/__tests__/wails-bridge-refusal.ts', import.meta.url),
        ),
      },
    ],
  },
})
