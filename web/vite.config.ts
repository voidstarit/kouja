import { defineConfig } from "vite";
import preact from "@preact/preset-vite";

export default defineConfig({
  plugins: [preact()],
  base: "/",
  build: {
    target: "es2022",
    modulePreload: { polyfill: false },
    outDir: "../internal/adminui/assets",
    emptyOutDir: false,
  },
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:9444",
        changeOrigin: true,
        headers: {
          Origin: "http://127.0.0.1:9444",
        },
      },
    },
  },
});
