import assert from "node:assert/strict";
import test from "node:test";
import { parseHTML } from "linkedom";

import {
  privateConsentEligible,
  privateConsentValue,
  renderCustomProviderCards,
  renderProviderCards,
} from "../src/providers/view.ts";

function container() {
  const { document } = parseHTML("<!doctype html><html><body><main></main></body></html>");
  return document.querySelector("main");
}

test("runtime custom-provider fields remain text and DOM attributes", () => {
  const attack = `<img src=x onerror="alert(1)">\" autofocus onfocus="alert(2)`;
  const root = container();
  renderCustomProviderCards(root, [{
    id: attack,
    subjectId: "subject-1",
    environmentId: "environment-1",
    name: attack,
    key: attack,
    description: attack,
    status: attack,
    allowedActions: [attack],
    lastIngestedAt: attack,
    createdAt: "2026-08-12T00:00:00Z",
    updatedAt: "2026-08-12T00:00:00Z",
  }]);

  assert.equal(root.querySelector("img"), null);
  assert.equal(root.querySelector("[autofocus]"), null);
  assert.match(root.textContent, /<img src=x onerror="alert\(1\)">/);
  const select = root.querySelector('[data-action="select-custom"]');
  assert.equal(select.dataset.id, attack);
  assert.equal(select.dataset.name, attack);
  assert.equal(select.dataset.customAction, attack);
  const toggle = root.querySelector('[data-action="toggle-custom"]');
  assert.equal(toggle.dataset.status, "disabled");
  assert.equal(root.dataset.state, "ready");
});

test("renders an explicit custom-provider empty state", () => {
  const root = container();
  renderCustomProviderCards(root, []);
  assert.ok(root.querySelector(".empty-list"));
  assert.match(root.textContent, /아직 만든 데이터 소스가 없어요/);
  assert.equal(root.dataset.state, "ready");
});

function provider(overrides = {}) {
  return {
    id: "github",
    name: "GitHub",
    category: "git-hosting",
    authMethods: ["oauth2", "token", "none"],
    supportsPrivateData: true,
    supportsScheduledSync: true,
    ...overrides,
  };
}

test("renders explicit unchecked private consent only for eligible providers", () => {
  const root = container();
  renderProviderCards(root, [provider()], []);
  const consent = root.querySelector('[name="includePrivate"]');
  assert.ok(consent);
  assert.equal(consent.type, "checkbox");
  assert.equal(consent.required, true);
  assert.equal(consent.hasAttribute("checked"), false);
  assert.equal(consent.getAttribute("aria-required"), "true");
  assert.equal(root.dataset.state, "ready");
  const help = root.querySelector(`#${consent.getAttribute("aria-describedby")}`);
  assert.match(help.textContent, /비공개 저장소의 활동 날짜와 집계량/);

  renderProviderCards(root, [provider({ supportsPrivateData: false })], []);
  assert.equal(root.querySelector('[name="includePrivate"]'), null);
});

test("provider runtime strings cannot create DOM nodes or event attributes", () => {
  const attack = `<img src=x onerror="alert(1)">\" autofocus onfocus="alert(2)`;
  const root = container();
  renderProviderCards(root, [provider({ id: attack, name: attack })], []);

  assert.equal(root.querySelector("img"), null);
  assert.equal(root.querySelector("[autofocus]"), null);
  assert.match(root.textContent, /<img src=x onerror="alert\(1\)">/);
  const form = root.querySelector("[data-provider-connect]");
  assert.equal(form.dataset.providerConnect, attack);
  assert.equal(form.querySelector("select").getAttribute("aria-label"), `${attack} 연결 방식`);
});

test("private collection requires provider capability, credential auth, and a check", () => {
  for (const method of ["oauth2", "token"]) {
    assert.equal(privateConsentEligible(true, method), true);
    assert.equal(privateConsentValue(true, method, false), false);
    assert.equal(privateConsentValue(true, method, true), true);
    assert.equal(privateConsentValue(false, method, true), false);
  }
  assert.equal(privateConsentEligible(true, "none"), false);
  assert.equal(privateConsentValue(true, "none", true), false);
});

test("hides consent when a public method is initially selected", () => {
  const root = container();
  renderProviderCards(root, [provider({ authMethods: ["none", "oauth2"] })], []);
  const consent = root.querySelector('[name="includePrivate"]');
  assert.equal(consent.closest(".private-consent-field").hidden, true);
  assert.equal(consent.required, false);
  assert.equal(consent.getAttribute("aria-required"), "false");
});
