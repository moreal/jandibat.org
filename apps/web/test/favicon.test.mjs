import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const webRoot = new URL("../", import.meta.url);
const publicRoot = new URL("public/", webRoot);
const indexHtml = readFileSync(new URL("index.html", webRoot), "utf8");
const manifest = JSON.parse(readFileSync(new URL("site.webmanifest", publicRoot), "utf8"));

function publicAsset(path) {
  return readFileSync(new URL(path.replace(/^\//, ""), publicRoot));
}

function pngDimensions(path) {
  const image = publicAsset(path);
  assert.equal(image.subarray(0, 8).toString("hex"), "89504e470d0a1a0a");
  return [image.readUInt32BE(16), image.readUInt32BE(20)];
}

test("the document advertises standard favicon and install metadata", () => {
  assert.match(indexHtml, /<meta name="application-name" content="jandibat" \/>/);
  assert.match(indexHtml, /<link rel="icon" href="\/favicon\.ico" type="image\/vnd\.microsoft\.icon" sizes="16x16 32x32 48x48" \/>/);
  assert.match(indexHtml, /<link rel="icon" href="\/favicon\.svg" type="image\/svg\+xml" sizes="any" \/>/);
  assert.match(indexHtml, /<link rel="apple-touch-icon" href="\/apple-touch-icon\.png" sizes="180x180" \/>/);
  assert.match(indexHtml, /<link rel="manifest" href="\/site\.webmanifest" \/>/);
});

test("favicon assets match their declared sizes and manifest purposes", () => {
  assert.equal(manifest.id, "/");
  assert.equal(manifest.start_url, "/");
  assert.equal(manifest.scope, "/");
  assert.equal(manifest.theme_color, "#f6f5ee");
  assert.equal(manifest.background_color, "#f6f5ee");
  assert.deepEqual(
    manifest.icons.map(({ src, sizes, type, purpose }) => ({ src, sizes, type, purpose })),
    [
      { src: "/app-icon.svg", sizes: "any", type: "image/svg+xml", purpose: "any maskable" },
      { src: "/web-app-manifest-192x192.png", sizes: "192x192", type: "image/png", purpose: "any maskable" },
      { src: "/web-app-manifest-512x512.png", sizes: "512x512", type: "image/png", purpose: "any maskable" },
    ],
  );

  assert.deepEqual(pngDimensions("apple-touch-icon.png"), [180, 180]);
  assert.deepEqual(pngDimensions("web-app-manifest-192x192.png"), [192, 192]);
  assert.deepEqual(pngDimensions("web-app-manifest-512x512.png"), [512, 512]);

  const icon = publicAsset("favicon.ico");
  assert.equal(icon.readUInt16LE(0), 0);
  assert.equal(icon.readUInt16LE(2), 1);
  assert.equal(icon.readUInt16LE(4), 3);
  assert.deepEqual([icon[6], icon[22], icon[38]], [16, 32, 48]);
});
