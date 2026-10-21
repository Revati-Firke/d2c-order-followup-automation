import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  publicDir: "public",
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    proxy: { "/api": process.env.VITE_API_TARGET || "http://localhost:3000" }
  }
});