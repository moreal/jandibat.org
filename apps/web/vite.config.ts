import solid from "@solidjs/vite-plugin";
import { fileRoutes } from "filesystem-routing/vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [
    solid({ start: true }),
    fileRoutes(),
  ],
});
