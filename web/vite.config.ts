/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build output is written where the Go binary embeds it from
// (internal/webui/dist). In dev, /api is proxied to the Go server.
//
// emptyOutDir is false so the committed `.gitkeep` in that directory survives a
// build — it is the one tracked file that keeps `//go:embed all:dist` compiling
// on a checkout that has never run a frontend build. The `prebuild` script
// clears stale hashed assets/ first, so builds are still clean in practice.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: false,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: process.env.ABR_DEV_API ?? "http://127.0.0.1:8674",
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
    coverage: {
      provider: "v8",
      include: ["src/**/*.{ts,tsx}"],
      exclude: ["src/**/*.test.{ts,tsx}", "src/test/**", "src/main.tsx"],
    },
  },
});
