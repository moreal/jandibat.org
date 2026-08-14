import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import test from "node:test";

const webRoot = new URL("../", import.meta.url);

test("uses the official Solid 2 start-mode entry and no legacy SPA bootstrap", () => {
  const packageJson = JSON.parse(readFileSync(new URL("package.json", webRoot), "utf8"));
  const viteConfig = readFileSync(new URL("vite.config.ts", webRoot), "utf8");

  assert.equal(packageJson.dependencies["solid-js"], "2.0.0-rc.0");
  assert.equal(packageJson.dependencies["@solidjs/web"], "2.0.0-rc.0");
  assert.equal(packageJson.devDependencies["@solidjs/vite-plugin"], "3.0.0-next.28");
  assert.match(viteConfig, /solid\(\{ start: true \}\)/);
  assert.equal(existsSync(new URL("index.html", webRoot)), false);
  assert.equal(existsSync(new URL("src/bootstrap.ts", webRoot)), false);
  assert.equal(existsSync(new URL("src/main.ts", webRoot)), false);
});
