import { readFile } from "node:fs/promises";

const reviewPath = process.argv[2];
const expectedSHA = process.argv[3] ?? process.env.SECURITY_REVIEW_SHA ?? "";
if (!reviewPath) throw new Error("usage: node scripts/validate-security-review.mjs <review.md> [reviewed-commit-sha]");
if (expectedSHA && !isSHA(expectedSHA)) throw new Error("expected reviewed commit SHA must be exactly 40 hexadecimal characters");

const groups = { COM: 8, MAG: 6, WEB: 6, OAU: 7, PRV: 6, SVG: 5, OPS: 8 };
const expected = new Set(Object.entries(groups).flatMap(([prefix, count]) =>
  Array.from({ length: count }, (_, index) => `${prefix}-${String(index + 1).padStart(2, "0")}`)));
const text = await readFile(reviewPath, "utf8");
const failures = [];
const seen = new Set();

for (const line of text.split(/\r?\n/u)) {
  const match = line.match(/^\|\s*((?:COM|MAG|WEB|OAU|PRV|SVG|OPS)-\d{2})\s*\|\s*(PASS|FAIL|N\/A|NOT RUN)\s*\|\s*([^|]+?)\s*\|\s*([^|]+?)\s*\|\s*([^|]+?)\s*\|\s*$/u);
  if (!match) continue;
  const [, id, status, rawEvidence, rawReviewer, rawReviewedAt] = match;
  const evidence = rawEvidence.trim();
  const reviewer = rawReviewer.trim();
  const reviewedAt = rawReviewedAt.trim();
  if (seen.has(id)) failures.push(`${id}: duplicate row`);
  seen.add(id);
  if (!expected.has(id)) failures.push(`${id}: unknown control`);
  if (status !== "PASS" && status !== "N/A") failures.push(`${id}: release-blocking status ${status}`);
  if (status === "PASS" && !isImmutableEvidence(evidence)) failures.push(`${id}: PASS requires an immutable run/artifact URL with digest or command+artifact digest`);
  if (status === "N/A" && !isApprovedExemption(evidence)) failures.push(`${id}: N/A requires a GitHub issue/PR URL and a concrete rationale`);
  if (!/^@[A-Za-z0-9](?:[A-Za-z0-9-]{0,37})$/u.test(reviewer)) failures.push(`${id}: reviewer must be a GitHub login such as @security-owner`);
  if (!isUTC(reviewedAt)) failures.push(`${id}: Reviewed at UTC must be a real YYYY-MM-DDTHH:mm:ssZ timestamp`);
}

for (const id of expected) if (!seen.has(id)) failures.push(`${id}: row is missing`);
const metadata = new Map();
for (const field of ["Commit SHA", "Scope", "Security owner"]) {
  const match = text.match(new RegExp(`^\\|\\s*${field}\\s*\\|\\s*([^|]+?)\\s*\\|\\s*$`, "mu"));
  if (!match) failures.push(`${field}: metadata is missing`);
  else metadata.set(field, match[1].trim());
}
const commitSHA = metadata.get("Commit SHA") ?? "";
if (!isSHA(commitSHA)) failures.push("Commit SHA: must be exactly 40 hexadecimal characters");
if (expectedSHA && commitSHA.toLowerCase() !== expectedSHA.toLowerCase()) failures.push("Commit SHA: does not match the reviewed source commit");
if ((metadata.get("Scope") ?? "").length < 3 || /^(?:NOT RUN|TODO|TBD|N\/A|-)$/iu.test(metadata.get("Scope") ?? "")) failures.push("Scope: concrete scope is required");
if (!/^@[A-Za-z0-9](?:[A-Za-z0-9-]{0,37})$/u.test(metadata.get("Security owner") ?? "")) failures.push("Security owner: must be a GitHub login");

if (failures.length > 0) {
  process.stderr.write(`security review is incomplete (${failures.length} problem(s)):\n${failures.map((failure) => `- ${failure}`).join("\n")}\n`);
  process.exitCode = 1;
} else {
  process.stdout.write(`security review is complete: ${reviewPath} (${expected.size} controls, commit ${commitSHA})\n`);
}

function isSHA(value) {
  return /^[0-9a-f]{40}$/iu.test(value);
}

function isImmutableEvidence(value) {
  const digest = "(?:sha256:|artifact-sha256:)[0-9a-f]{64}";
  const github = `https://github\\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/actions/runs/[0-9]+(?:/artifacts/[0-9]+)?(?:[ ;#]+${digest})`;
  const command = `command:[^;|\\r\\n]{3,240};[ ]*${digest}`;
  return new RegExp(`^(?:${github}|${command})$`, "iu").test(value);
}

function isApprovedExemption(value) {
  return /^issue:https:\/\/github\.com\/[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+\/(?:issues|pull)\/[0-9]+;\s*rationale:.{12,500}$/iu.test(value);
}

function isUTC(value) {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/u.test(value)) return false;
  const parsed = new Date(value);
  return !Number.isNaN(parsed.valueOf()) && parsed.toISOString().replace(".000Z", "Z") === value;
}
