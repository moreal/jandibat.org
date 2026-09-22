import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import test from "node:test";

const webRoot = new URL("../", import.meta.url);
const repoRoot = new URL("../../../", import.meta.url);

test("uses the official Solid 2 start-mode entry and no legacy SPA bootstrap", () => {
  const packageJson = JSON.parse(readFileSync(new URL("package.json", webRoot), "utf8"));
  const viteConfig = readFileSync(new URL("vite.config.ts", webRoot), "utf8");

  assert.equal(packageJson.dependencies["solid-js"], "2.0.0-rc.9");
  assert.equal(packageJson.dependencies["@solidjs/web"], "2.0.0-rc.9");
  assert.equal(packageJson.dependencies["@solidjs/router"], "2.0.0-next.26");
  assert.equal(packageJson.devDependencies["@solidjs/testing-library"], "1.0.0-beta.3");
  assert.equal(packageJson.devDependencies["@solidjs/vite-plugin"], "3.0.0-next.44");
  assert.match(viteConfig, /solid\(\{ start: true \}\)/);
  assert.equal(existsSync(new URL("index.html", webRoot)), false);
  assert.equal(existsSync(new URL("src/bootstrap.ts", webRoot)), false);
  assert.equal(existsSync(new URL("src/main.ts", webRoot)), false);
});

test("pins the workspace frontend toolchain versions", () => {
  const rootPackageJson = JSON.parse(
    readFileSync(new URL("package.json", repoRoot), "utf8"),
  );
  const webPackageJson = JSON.parse(
    readFileSync(new URL("apps/web/package.json", repoRoot), "utf8"),
  );
  const contractsPackageJson = JSON.parse(
    readFileSync(new URL("packages/contracts/package.json", repoRoot), "utf8"),
  );
  const sdkPackageJson = JSON.parse(
    readFileSync(new URL("packages/custom-provider-sdk/package.json", repoRoot), "utf8"),
  );

  assert.equal(rootPackageJson.packageManager, "yarn@4.18.0");
  assert.equal(webPackageJson.devDependencies.typescript, "7.0.2");
  assert.equal(webPackageJson.devDependencies.vite, "8.3.0");
  assert.equal(webPackageJson.devDependencies.vitest, "5.0.1");
  assert.equal(contractsPackageJson.devDependencies.typescript, "7.0.2");
  assert.equal(sdkPackageJson.devDependencies.typescript, "7.0.2");
});
