import { vitePreprocess } from '@sveltejs/vite-plugin-svelte'

export default {
  // 当前组件只使用原生 CSS；关闭样式预处理可避免 svelte-check 为每个
  // <style> 重复加载 Vite 配置并向工作区父目录探测。TypeScript 仍由
  // Svelte/Vite 正常处理。
  preprocess: vitePreprocess({ style: false }),
}
