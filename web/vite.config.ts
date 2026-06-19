import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In dev, the API runs on :8080. The dashboard always uses relative paths
// (/api/..., /healthz, /metrics), so Vite proxies those to the Go server here,
// and Caddy proxies the same paths in production. The frontend never hard-codes
// a backend host.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/ingest": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
      "/metrics": "http://localhost:8080",
    },
  },
  build: { outDir: "dist" },
});
