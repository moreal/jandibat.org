import assert from "node:assert/strict";
import test from "node:test";

import {
  createSubjectInput,
  preferredOwnedSubject,
  SubjectInputError,
} from "../src/subjects/onboarding.ts";

const subject = (handle) => ({
  id: `subject-${handle}`,
  handle,
  displayName: handle.toUpperCase(),
  timezone: "Asia/Seoul",
  isPublic: true,
  createdAt: "2026-08-12T00:00:00Z",
  updatedAt: "2026-08-12T00:00:00Z",
});

test("normalizes a create-subject request to the generated contract", () => {
  assert.deepEqual(
    createSubjectInput("  my.garden-1  ", "  나의 잔디밭  ", "Asia/Seoul"),
    {
      handle: "my.garden-1",
      displayName: "나의 잔디밭",
      timezone: "Asia/Seoul",
      isPublic: true,
    },
  );
  assert.deepEqual(createSubjectInput("garden", " ", "UTC"), {
    handle: "garden",
    displayName: undefined,
    timezone: "UTC",
    isPublic: true,
  });
});

test("rejects handles and display names outside the OpenAPI constraints", () => {
  for (const handle of ["", "-garden", "garden space", "a".repeat(65)]) {
    assert.throws(
      () => createSubjectInput(handle, "Garden", "UTC"),
      (error) => error instanceof SubjectInputError && error.field === "handle",
    );
  }
  assert.throws(
    () => createSubjectInput("garden", "가".repeat(101), "UTC"),
    (error) => error instanceof SubjectInputError && error.field === "displayName",
  );
  assert.throws(
    () => createSubjectInput("garden", "Garden", ""),
    (error) => error instanceof SubjectInputError && error.field === "timezone",
  );
});

test("restores only an owned persisted subject", () => {
  const subjects = [subject("alpha"), subject("beta")];
  assert.equal(preferredOwnedSubject(subjects, "beta")?.handle, "beta");
  assert.equal(preferredOwnedSubject(subjects, "not-owned"), undefined);
});

test("auto-selects a sole subject but requires a choice for multiple subjects", () => {
  assert.equal(preferredOwnedSubject([subject("only")], null)?.handle, "only");
  assert.equal(
    preferredOwnedSubject([subject("alpha"), subject("beta")], null),
    undefined,
  );
  assert.equal(preferredOwnedSubject([], "alpha"), undefined);
});
