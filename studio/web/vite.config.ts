import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// 개발 중에는 API를 검토 백엔드(studio_review, 루프백 18480)로 넘긴다.
export default defineConfig({
  plugins: [react()],
  server: { host: "127.0.0.1", port: 5173, proxy: { "/api": "http://127.0.0.1:18480" } },
});
