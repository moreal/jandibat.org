import solid from "@solidjs/vite-plugin";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [solid()],
  test: {
    environment: "happy-dom",
    include: ["test/**/*.test.tsx"],
    restoreMocks: true,
  },
});
