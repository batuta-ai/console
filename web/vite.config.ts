import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: { outDir: "../internal/webui/dist", emptyOutDir: true },
  server: { proxy: { "/v0": { target: "http://127.0.0.1:9477", ws: true } } },
});
