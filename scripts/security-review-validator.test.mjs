import assert from "node:assert/strict";
import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const sha = "a".repeat(40);
const digest = "b".repeat(64);
const ids = Object.entries({ COM: 8, MAG: 6, WEB: 6, OAU: 7, PRV: 6, SVG: 5, OPS: 8 })
  .flatMap(([prefix, count]) => Array.from({ length: count }, (_, index) => `${prefix}-${String(index + 1).padStart(2, "0")}`));

test("accepts digest-bound evidence for an exact reviewed SHA", async () => {
  assert.equal(run(await fixture()).status, 0);
});

test("rejects shortened or mismatched commit SHAs", async () => {
  assert.notEqual(run(await fixture({ commit: "abc" })).status, 0);
  assert.notEqual(run(await fixture(), "c".repeat(40)).status, 0);
});

test("rejects unauthenticated PASS prose and unapproved N/A", async () => {
  assert.notEqual(run(await fixture({ evidence: "tests passed" })).status, 0);
  assert.notEqual(run(await fixture({ status: "N/A", evidence: "not applicable" })).status, 0);
});

async function fixture({ commit = sha, status = "PASS", evidence = `command:make ci; artifact-sha256:${digest}` } = {}) {
  const directory = await mkdtemp(join(tmpdir(), "security-review-"));
  const path = join(directory, "review.md");
  const rows = ids.map((id) => `| ${id} | ${status} | ${evidence} | @security-owner | 2026-08-13T00:00:00Z |`).join("\n");
  await writeFile(path, `| Metadata | Value |\n| --- | --- |\n| Commit SHA | ${commit} |\n| Scope | security regression |\n| Security owner | @security-owner |\n\n| Control | Status | Evidence or N/A rationale | Reviewer | Reviewed at UTC |\n| --- | --- | --- | --- | --- |\n${rows}\n`);
  return path;
}

function run(path, expected = sha) {
  return spawnSync(process.execPath, ["scripts/validate-security-review.mjs", path, expected], { encoding: "utf8" });
}
