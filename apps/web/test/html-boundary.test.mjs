import assert from "node:assert/strict";
import test from "node:test";

import {
  escapeHtml,
  escapeHtmlAttribute,
  escapeMarkdownDestination,
  escapeMarkdownLabel,
} from "../src/ui/html.ts";

test("escapes untrusted strings inserted into HTML text and attributes", () => {
  assert.equal(
    escapeHtml(`<img src=x onerror="alert('x')">`),
    "&lt;img src=x onerror=&quot;alert(&#039;x&#039;)&quot;&gt;",
  );
});

test("escapes quotes and control whitespace in dynamic HTML attributes", () => {
  assert.equal(
    escapeHtmlAttribute(`value"\n\tonfocus="alert(1)`),
    "value&quot;&#10;&#9;onfocus=&quot;alert(1)",
  );
});

test("keeps an embed title inside its Markdown label", () => {
  assert.equal(
    escapeMarkdownLabel("title]\\\n[click me](javascript:alert(1))"),
    "title\\]\\\\ \\[click me\\](javascript:alert(1))",
  );
});

test("keeps a configured URL inside its Markdown destination", () => {
  assert.equal(
    escapeMarkdownDestination("https://api.example.test/a)b(c d"),
    "https://api.example.test/a%29b%28c%20d",
  );
});
