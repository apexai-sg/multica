import { fileURLToPath } from "node:url";
import { defineConfig, searchForWorkspaceRoot } from "vite";
import react from "@vitejs/plugin-react";

const appRoot = fileURLToPath(new URL("..", import.meta.url));

export default defineConfig({
  root: fileURLToPath(new URL(".", import.meta.url)),
  publicDir: fileURLToPath(new URL("../public", import.meta.url)),
  plugins: [react()],
  resolve: { alias: { "@": appRoot }, dedupe: ["react", "react-dom"] },
  css: { postcss: appRoot },
  server: {
    host: "127.0.0.1",
    port: 4311,
    strictPort: true,
    fs: { allow: [searchForWorkspaceRoot(appRoot)] },
  },
});
