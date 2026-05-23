import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { resolve } from 'path'

export default defineConfig({
  plugins: [react()],
  build: {
    lib: {
      entry: resolve(import.meta.dirname, 'src/index.js'),
      name: 'VulosOsMeetApp',
      formats: ['es'],
      fileName: () => 'index.js',
    },
    outDir: 'dist',
    sourcemap: false,
    rollupOptions: {
      external: ['react', 'react-dom', 'react/jsx-runtime', '@vulos/office-client'],
    },
  },
})
