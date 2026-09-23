import assert from "node:assert/strict";
import test from "node:test";

import { buildEmbedValues } from "../src/embed/model.ts";

test("builds browser-route embed snippets without leaking unsafe markup", () => {
  const output = buildEmbedValues({
    subject: "my-garden",
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
    "https://api.example.test/base/v1/render/my-garden.svg?theme=github-dark&weekStart=monday&cellSize=32&showLegend=false",
  );
  assert.match(output.markdown, /https:\/\/app\.example\.test\/jandibat\/explore\/my-garden/);
  assert.equal(output.markdown.includes("\n"), false);
  assert.match(output.html, /href="https:\/\/app\.example\.test\/jandibat\/explore\/my-garden"/);
  assert.match(output.html, /&lt;img src=x onerror=&quot;alert\(1\)&quot;&gt;/);
  assert.equal(output.html.includes("<img src=x onerror="), false);
});

test("rejects blank or malformed persisted subject handles before building an SVG URL", () => {
  const options = {
    subject: "",
    title: "Activity",
    theme: "system",
    weekStart: "sunday",
    cellSize: 11,
    showLegend: true,
    apiBaseUrl: "https://api.example.test/prefix",
    appBaseUrl: "https://app.example.test/jandibat/",
  };
  for (const subject of ["", " \r\n\t ", "bad handle", "../garden", "_garden", "a".repeat(65)]) {
    assert.equal(buildEmbedValues({ ...options, subject }), undefined, subject);
  }
  assert.equal(
    buildEmbedValues({ ...options, subject: "  garden-1  " })?.url,
    "https://api.example.test/prefix/v1/render/garden-1.svg?theme=system&weekStart=sunday&cellSize=11&showLegend=true",
  );
});

test("escapes control whitespace in HTML attributes and Markdown labels", () => {
  const output = buildEmbedValues({
    subject: "garden",
    title: 'A"\r\n\tB',
    theme: "system",
    weekStart: "sunday",
    cellSize: 11,
    showLegend: true,
    apiBaseUrl: "https://api.example.test/prefix",
    appBaseUrl: "https://app.example.test/jandibat/",
  });
  assert.match(output.html, /alt="A&quot;&#13;&#10;&#9;B"/);
  assert.match(output.markdown, /\[!\[A" B\]/);
  assert.equal(output.markdown.includes("\r"), false);
  assert.equal(output.markdown.includes("\n"), false);
  assert.equal(output.markdown.includes("\t"), false);
});
