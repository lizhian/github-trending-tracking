import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
const apiProxyTarget = process.env.API_PROXY_TARGET;
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": {
        target: apiProxyTarget ?? "http://127.0.0.1:8080",
        changeOrigin: true,
        ...(apiProxyTarget ? { headers: { origin: new URL(apiProxyTarget).origin } } : {}),
      },
    },
  },
});
