import assert from "node:assert/strict";
import test from "node:test";

import { buildEmbedValues } from "../src/embed/model.ts";

test("builds browser-route embed snippets without leaking unsafe markup", () => {
  const output = buildEmbedValues({
    subject: "my garden",
    title: `title]\\\n<img src=x onerror="alert(1)">`,
    theme: "github-dark",
    weekStart: "monday",
    cellSize: 99,
    showLegend: false,
    apiBaseUrl: "https://api.example.test/base/",
    appBaseUrl: "https://app.example.test/jandibat/",
  });

  assert.equal(
    output.url,
    "https://api.example.test/base/v1/render/my%20garden.svg?theme=github-dark&weekStart=monday&cellSize=32&showLegend=false",
  );
  assert.match(output.markdown, /https:\/\/app\.example\.test\/jandibat\/explore\/my%20garden/);
  assert.equal(output.markdown.includes("\n"), false);
  assert.match(output.html, /href="https:\/\/app\.example\.test\/jandibat\/explore\/my%20garden"/);
  assert.match(output.html, /&lt;img src=x onerror=&quot;alert\(1\)&quot;&gt;/);
  assert.equal(output.html.includes("<img src=x onerror="), false);
});
