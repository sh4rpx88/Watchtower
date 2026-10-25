import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In dev, API calls go to the panel on :8080.
export default defineConfig({
  plugins: [react()],
  server: { proxy: { "/api": "http://localhost:8080" } },
});
