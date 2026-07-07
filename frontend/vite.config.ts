import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:1983',
        changeOrigin: true,
      },
    },
  },
  build: {
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('node_modules')) {
            if (id.includes('echarts') || id.includes('zrender')) return 'echarts-vendor';
            if (id.includes('three') || id.includes('force-graph') || id.includes('d3-')) return 'graph-vendor';
            if (id.includes('react')) return 'react-vendor';
            if (id.includes('lucide')) return 'lucide-vendor';
            return 'vendor';
          }
        }
      }
    },
    chunkSizeWarningLimit: 800,
  }
})
