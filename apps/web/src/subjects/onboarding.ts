import type { CreateSubjectDto, SubjectDto } from "@jandibat/contracts";

export const SUBJECT_HANDLE_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

export class SubjectInputError extends Error {
  readonly field: "handle" | "displayName" | "timezone";

  constructor(
    field: SubjectInputError["field"],
    message: string,
  ) {
    super(message);
    this.name = "SubjectInputError";
    this.field = field;
  }
}

export function createSubjectInput(
  handleValue: string,
  displayNameValue: string,
  timezoneValue: string,
): CreateSubjectDto {
  const handle = handleValue.trim();
  const displayName = displayNameValue.trim();
  const timezone = timezoneValue.trim();

  if (!handle) {
    throw new SubjectInputError("handle", "프로필 식별자를 입력해 주세요.");
  }
  if (handle.length > 64 || !SUBJECT_HANDLE_PATTERN.test(handle)) {
    throw new SubjectInputError(
      "handle",
      "식별자는 64자 이내의 영문자·숫자로 시작하고 영문자, 숫자, 점, 밑줄, 하이픈만 사용할 수 있어요.",
    );
  }
  if (displayName.length > 100) {
    throw new SubjectInputError("displayName", "표시 이름은 100자 이내여야 해요.");
  }
  if (!timezone || timezone.length > 64) {
    throw new SubjectInputError("timezone", "사용할 수 있는 시간대를 확인하지 못했습니다.");
  }

  return {
    handle,
    displayName: displayName || undefined,
    timezone,
    isPublic: true,
  };
}

/**
 * Restores an explicit selection, or selects the only subject. Multiple
 * subjects intentionally require a user choice when no stored selection is
 * available.
 */
export function preferredOwnedSubject(
  subjects: readonly SubjectDto[],
  persistedHandle: string | null | undefined,
): SubjectDto | undefined {
  const persisted = persistedHandle?.trim();
  if (persisted) {
    const match = subjects.find((subject) => subject.handle === persisted);
    if (match) return match;
  }
  return subjects.length === 1 ? subjects[0] : undefined;
}
