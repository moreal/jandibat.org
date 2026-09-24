import type { SubjectDto } from "@jandibat/contracts";
import {
  Match,
  Switch,
  createContext,
  createSignal,
  onSettled,
  useContext,
  type Accessor,
  type Element,
  type Setter,
} from "solid-js";
import { readCurrentViewer, readOwnedSubjects, SignedOutError } from "../auth/owner-viewer";
import { useAppState } from "../app/state";
import { useAuthEpoch } from "../relay/auth-epoch";
import { createRelayMutation } from "../relay";
import { ErrorCallout, Icon, PageIntro, errorMessage } from "../components/common";
import {
  createSubjectInput,
  preferredOwnedSubject,
  SubjectInputError,
} from "./onboarding";
import type { AuthCreateSubjectMutation } from "../pages/__generated__/AuthCreateSubjectMutation.graphql";
import createSubjectMutation from "../pages/__generated__/AuthCreateSubjectMutation.graphql";

type OwnerStatus =
  | { kind: "loading" }
  | { kind: "ready" }
  | { kind: "signed-out" }
  | { kind: "create" }
  | { kind: "select" }
  | { kind: "error"; error: unknown };

export type OwnerSubjects = {
  subjects: Accessor<readonly SubjectDto[]>;
  setSubjects: Setter<readonly SubjectDto[]>;
};

export const OwnerSubjectsContext = createContext<OwnerSubjects>();

export function useOwnerSubjects(): OwnerSubjects {
  const owner = useContext(OwnerSubjectsContext);
  if (!owner) throw new Error("Owner subjects require a route-scoped Relay viewer.");
  return owner;
}

function deviceTimezone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

export function OwnerSubjectSelect(props: { onSelected?: (handle: string) => void }) {
  const app = useAppState();
  const owner = useOwnerSubjects();

  const select = (event: SubmitEvent) => {
    event.preventDefault();
    const handle = String(new FormData(event.currentTarget as HTMLFormElement).get("subject") ?? "");
    const subject = owner.subjects().find((candidate) => candidate.handle === handle);
    if (!subject) return;
    app.selectOwnerSubject(subject);
    props.onSelected?.(subject.handle);
  };

  return (
    <form class="connection-toolbar" onSubmit={select}>
      <label for="owner-subject-select"><strong>관리할 잔디밭</strong></label>
      <select id="owner-subject-select" name="subject" required>
        <option value="" disabled selected={!app.currentSubject()}>선택해 주세요</option>
        {owner.subjects().map((subject) => (
          <option value={subject.handle} selected={subject.handle === app.currentSubject()}>
            {subject.displayName || `@${subject.handle}`}
          </option>
        ))}
      </select>
      <button class="secondary-button" type="submit">선택</button>
    </form>
  );
}

export function OwnerSubjectCreator(props: { onCreated?: (subject: SubjectDto) => void }) {
  const app = useAppState();
  const owner = useOwnerSubjects();
  const createSubject = createRelayMutation<AuthCreateSubjectMutation>(createSubjectMutation);
  const [busy, setBusy] = createSignal(false);
  const [failure, setFailure] = createSignal<{
    message: string;
    field?: "handle" | "displayName" | "timezone";
  }>();

  const submit = async (event: SubmitEvent) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const values = new FormData(form);
    setFailure(undefined);

    let input;
    try {
      input = createSubjectInput(
        String(values.get("handle") ?? ""),
        String(values.get("displayName") ?? ""),
        deviceTimezone(),
      );
    } catch (error) {
      const field = error instanceof SubjectInputError ? error.field : undefined;
      setFailure({ message: errorMessage(error), field });
      if (field) {
        const inputElement = form.elements.namedItem(field);
        if (inputElement instanceof HTMLElement) inputElement.focus();
      }
      return;
    }

    setBusy(true);
    try {
      const result = await createSubject({ input });
      if (result.createSubject.errors.length || !result.createSubject.subject) {
        throw new Error(result.createSubject.errors[0]?.message ?? "잔디밭을 만들지 못했습니다.");
      }
      const created: SubjectDto = {
        ...result.createSubject.subject,
        displayName: result.createSubject.subject.displayName ?? undefined,
      };
      owner.setSubjects((subjects) => [...subjects, created]);
      app.selectOwnerSubject(created);
      app.showToast(`@${created.handle} 잔디밭을 만들었습니다.`);
      props.onCreated?.(created);
    } catch (error) {
      setFailure({ message: errorMessage(error) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="form-card">
      <p class="eyebrow">Create your garden</p>
      <h2>첫 잔디밭을 만들어 주세요.</h2>
      <p>Provider와 커스텀 데이터를 연결할 공개 프로필을 먼저 만듭니다.</p>
      <form class="stack-form" novalidate onSubmit={submit}>
        <label for="owner-subject-handle">프로필 식별자</label>
        <div class="input-prefix">
          <span>@</span>
          <input
            id="owner-subject-handle"
            name="handle"
            autocomplete="username"
            maxlength="64"
            pattern="[A-Za-z0-9][A-Za-z0-9._-]*"
            placeholder="my-garden"
            aria-invalid={failure()?.field === "handle" ? "true" : undefined}
            required
          />
        </div>
        <small>영문자·숫자로 시작하고 점, 밑줄, 하이픈을 사용할 수 있어요.</small>
        <label for="owner-subject-display-name">표시 이름 <span>(선택)</span></label>
        <input
          id="owner-subject-display-name"
          name="displayName"
          maxlength="100"
          placeholder="나의 잔디밭"
          aria-invalid={failure()?.field === "displayName" ? "true" : undefined}
        />
        <p class="inline-error" role="alert" hidden={!failure()}>{failure()?.message}</p>
        <button
          class="primary-button wide"
          type="submit"
          disabled={busy()}
          aria-busy={busy() ? "true" : undefined}
        >
          {busy() ? "만드는 중…" : <>잔디밭 만들기 <Icon name="arrow" /></>}
        </button>
      </form>
    </div>
  );
}

function OwnerWorkspaceIntro() {
  return (
    <PageIntro
      eyebrow="Owner workspace"
      title={<>내 잔디밭을<br /><em>안전하게 관리하세요.</em></>}
      description="인증된 계정이 소유한 프로필만 연결과 커스텀 데이터에 사용할 수 있습니다."
    />
  );
}

export function OwnerGate(props: { children: (subject: string) => Element }) {
  const app = useAppState();
  const authEpoch = useAuthEpoch();
  const [subjects, setSubjects] = createSignal<readonly SubjectDto[]>([]);
  const [status, setStatus] = createSignal<OwnerStatus>({ kind: "loading" });
  let controller: AbortController | undefined;

  const load = async () => {
    controller?.abort();
    const requestController = new AbortController();
    controller = requestController;
    setStatus({ kind: "loading" });
    try {
      const viewer = await readCurrentViewer(authEpoch.environment(), requestController.signal);
      if (requestController.signal.aborted) return;
      if (authEpoch.authenticated(viewer.user.id)) return;
      const subjects = await readOwnedSubjects(authEpoch.environment(), requestController.signal);
      if (requestController.signal.aborted) return;
      setSubjects(subjects);
      const selected = preferredOwnedSubject(subjects, app.currentSubject());
      if (selected) {
        app.selectOwnerSubject(selected);
        setStatus({ kind: "ready" });
        return;
      }
      app.clearOwnerSubject();
      setStatus({ kind: subjects.length === 0 ? "create" : "select" });
    } catch (error) {
      if (requestController.signal.aborted || error instanceof DOMException && error.name === "AbortError") return;
      if (error instanceof SignedOutError) {
        if (authEpoch.signedOut()) return;
        setSubjects([]);
        app.clearOwnerSubject();
        setStatus({ kind: "signed-out" });
        return;
      }
      setStatus({ kind: "error", error });
    }
  };

  onSettled(() => {
    void load();
    return () => controller?.abort();
  });

  return <OwnerSubjectsContext value={{ subjects, setSubjects }}>
    <Switch>
      <Match when={status().kind === "loading"}>
        <section class="page section-shell narrow">
          <div class="heatmap-loading" role="status"><p>소유한 잔디밭을 확인하는 중…</p></div>
        </section>
      </Match>
      <Match when={status().kind === "ready"}>{props.children(app.currentSubject())}</Match>
      <Match when={status().kind === "signed-out"}>
        <section class="page section-shell narrow">
          <OwnerWorkspaceIntro />
          <div class="form-card">
            <p class="eyebrow">Sign in required</p>
            <h2>로그인 후 관리할 수 있어요.</h2>
            <p>소유한 잔디밭을 확인한 뒤에만 Provider와 커스텀 데이터 요청을 보냅니다.</p>
            <a class="primary-button wide" href="/auth">로그인하기 <Icon name="arrow" /></a>
          </div>
        </section>
      </Match>
      <Match when={status().kind === "create"}>
        <section class="page section-shell narrow">
          <OwnerWorkspaceIntro />
          <OwnerSubjectCreator onCreated={() => setStatus({ kind: "ready" })} />
        </section>
      </Match>
      <Match when={status().kind === "select"}>
        <section class="page section-shell narrow">
          <OwnerWorkspaceIntro />
          <div class="form-card">
            <p class="eyebrow">Choose your garden</p>
            <h2>관리할 잔디밭을 선택해 주세요.</h2>
            <p>선택하기 전에는 소유자 전용 기능이 비활성화됩니다.</p>
            <OwnerSubjectSelect onSelected={() => setStatus({ kind: "ready" })} />
          </div>
        </section>
      </Match>
      <Match when={status().kind === "error"}>
        <section class="page section-shell narrow">
          <ErrorCallout
            error={(status() as Extract<OwnerStatus, { kind: "error" }>).error}
            retry={() => void load()}
          />
        </section>
      </Match>
    </Switch>
  </OwnerSubjectsContext>;
}
