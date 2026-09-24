import { Match, Show, Switch, createSignal, onSettled } from "solid-js";
import { api, ApiError, defaultApiBaseUrl } from "../api/client";
import { useAppState } from "../app/state";
import { readCurrentViewer, readOwnedSessions, readOwnedSubjects, SignedOutError, type OwnedSession } from "../auth/owner-viewer";
import {
  magicLinkLocationHasToken,
  magicLinkSafeLocation,
  magicLinkTokenFromUrl,
} from "../auth/magic-link";
import { createPasskey, getPasskey, passkeyAvailable } from "../auth/passkey";
import { useAuthEpoch, type AuthNotice } from "../relay/auth-epoch";
import { createRelayEphemeralMutation, createRelayMutation } from "../relay";
import { ErrorCallout, Icon, errorMessage, formatTime } from "../components/common";
import { legacyCallbackUrl, takePendingMagicLinkToken } from "../routing/legacy";
import { OwnerSubjectCreator, OwnerSubjectSelect, OwnerSubjectsContext, useOwnerSubjects } from "../subjects/OwnerGate";
import type { SubjectDto } from "@jandibat/contracts";
import { preferredOwnedSubject } from "../subjects/onboarding";
import type { AuthViewerQuery } from "./__generated__/AuthViewerQuery.graphql";
import type { AuthRequestMagicLinkMutation } from "./__generated__/AuthRequestMagicLinkMutation.graphql";
import requestMagicLinkMutation from "./__generated__/AuthRequestMagicLinkMutation.graphql";
import type { AuthBeginPasskeySignInMutation } from "./__generated__/AuthBeginPasskeySignInMutation.graphql";
import type { AuthFinishPasskeySignInMutation } from "./__generated__/AuthFinishPasskeySignInMutation.graphql";
import beginPasskeySignInMutation from "./__generated__/AuthBeginPasskeySignInMutation.graphql";
import finishPasskeySignInMutation from "./__generated__/AuthFinishPasskeySignInMutation.graphql";
import type { AuthSignOutMutation } from "./__generated__/AuthSignOutMutation.graphql";
import signOutMutation from "./__generated__/AuthSignOutMutation.graphql";
import type { AuthRevokeSessionMutation } from "./__generated__/AuthRevokeSessionMutation.graphql";
import revokeSessionMutation from "./__generated__/AuthRevokeSessionMutation.graphql";
import type { AuthBeginPasskeyRegistrationMutation } from "./__generated__/AuthBeginPasskeyRegistrationMutation.graphql";
import type { AuthFinishPasskeyRegistrationMutation } from "./__generated__/AuthFinishPasskeyRegistrationMutation.graphql";
import beginPasskeyRegistrationMutation from "./__generated__/AuthBeginPasskeyRegistrationMutation.graphql";
import finishPasskeyRegistrationMutation from "./__generated__/AuthFinishPasskeyRegistrationMutation.graphql";
import type { AuthRequestSubjectDeletionMutation } from "./__generated__/AuthRequestSubjectDeletionMutation.graphql";
import requestSubjectDeletionMutation from "./__generated__/AuthRequestSubjectDeletionMutation.graphql";

type CurrentViewer = NonNullable<AuthViewerQuery["response"]["viewer"]>;

type AuthState =
  | { kind: "loading" }
  | { kind: "signed-out" }
  | { kind: "signed-in"; auth: CurrentViewer }
  | { kind: "error"; error: unknown };

const authNoticeMessages: Record<AuthNotice, string> = {
  magic: "이메일을 확인하고 로그인했습니다.",
  passkey: "Passkey로 로그인했습니다.",
  "signed-out": "안전하게 로그아웃했습니다.",
};

function showPendingAuthNotice(
  notice: AuthNotice | undefined,
  showToast: (message: string) => void,
) {
  if (notice) showToast(authNoticeMessages[notice]);
}

function AuthStory(props: { authenticated: boolean }) {
  return (
    <div class="auth-story">
      <a class="mini-brand" href="/"><span class="brand-seed" aria-hidden="true" />jandibat.org</a>
      <div>
        <p class="eyebrow">{props.authenticated ? "Signed in" : "Welcome back"}</p>
        <h1>
          {props.authenticated ? (
            <>오늘의 기록을<br /><em>이어갈 시간이에요.</em></>
          ) : (
            <>기록은 이어질 때<br /><em>더 선명해집니다.</em></>
          )}
        </h1>
        <p>
          {props.authenticated
            ? "연결과 커스텀 데이터 소스를 관리하거나 새로운 Passkey를 등록하세요."
            : "비밀번호 없이 안전하게 로그인하고, 어디서든 나의 잔디밭을 이어가세요."}
        </p>
      </div>
      <blockquote>“꾸준함은 특별한 하루가 아니라<br />평범한 날들의 합입니다.”</blockquote>
    </div>
  );
}

function MagicLinkForm() {
  const app = useAppState();
  const requestMagicLink = createRelayEphemeralMutation<AuthRequestMagicLinkMutation>(
    defaultApiBaseUrl(), requestMagicLinkMutation,
  );
  const [busy, setBusy] = createSignal(false);
  const [failure, setFailure] = createSignal<string>();
  const [sentTo, setSentTo] = createSignal<string>();

  const submit = async (event: SubmitEvent) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    if (!form.reportValidity()) return;
    const email = String(new FormData(form).get("email") ?? "");
    setFailure(undefined);
    setBusy(true);
    try {
      const result = await requestMagicLink({ input: {
        email, redirectURI: legacyCallbackUrl("auth"),
      } });
      if (result.requestMagicLink.errors.length || !result.requestMagicLink.accepted) {
        throw new Error(result.requestMagicLink.errors[0]?.message ?? "로그인 링크를 보내지 못했습니다.");
      }
      setSentTo(email);
    } catch (error) {
      const message = errorMessage(error);
      setFailure(message);
      app.showToast(message, "error");
      setBusy(false);
    }
  };

  return (
    <Show
      when={sentTo()}
      fallback={(
        <form class="stack-form" novalidate onSubmit={submit}>
          <label for="email">이메일 주소</label>
          <input
            id="email"
            name="email"
            type="email"
            autocomplete="email"
            maxlength="320"
            placeholder="you@example.com"
            aria-describedby="magic-link-error"
            required
          />
          <p class="inline-error" id="magic-link-error" role="alert" hidden={!failure()}>
            {failure()}
          </p>
          <button
            class="primary-button wide"
            type="submit"
            disabled={busy()}
            aria-busy={busy() ? "true" : undefined}
          >
            {busy() ? "보내는 중…" : <>로그인 링크 받기 <Icon name="arrow" /></>}
          </button>
        </form>
      )}
    >
      {(email) => (
        <div class="success-state">
          <span aria-hidden="true">✓</span>
          <h3>이메일을 확인해 주세요.</h3>
          <p><strong>{email()}</strong>으로 로그인 링크를 보냈어요. 링크는 잠시 후 만료됩니다.</p>
        </div>
      )}
    </Show>
  );
}

function SignedOutCard(props: { onPasskeySignIn: () => Promise<void>; busy: boolean }) {
  const unavailable = !passkeyAvailable();
  return (
    <div class="auth-card">
      <p class="eyebrow">Sign in</p>
      <h2>다시 만나서 반가워요.</h2>
      <p>가장 편한 방법을 선택하세요.</p>
      <button
        class="passkey-button"
        type="button"
        disabled={unavailable || props.busy}
        aria-busy={props.busy ? "true" : undefined}
        onClick={() => void props.onPasskeySignIn()}
      >
        <span class="passkey-symbol" aria-hidden="true">⌁</span>
        <span>
          <strong>{props.busy ? "Passkey 확인 중…" : "Passkey로 로그인"}</strong>
          <small>{unavailable ? "이 환경에서는 사용할 수 없어요" : "Touch ID, Face ID 또는 보안 키"}</small>
        </span>
        <Icon name="arrow" />
      </button>
      <div class="or"><span>또는 이메일로</span></div>
      <MagicLinkForm />
      <p class="auth-help">처음이신가요? 이메일 링크로 계정이 자동 생성됩니다.</p>
      <details>
        <summary>이 기기에 Passkey 등록하기</summary>
        <p>먼저 이메일 링크로 로그인하세요. 로그인한 계정 화면에서 Passkey를 추가할 수 있습니다.</p>
      </details>
    </div>
  );
}

function SignedInCard(props: { auth: CurrentViewer; onSignedOut: () => void }) {
  const app = useAppState();
  const owner = useOwnerSubjects();
  const authEpoch = useAuthEpoch();
  const signOutMutationCommit = createRelayMutation<AuthSignOutMutation>(signOutMutation);
  const revokeSession = createRelayMutation<AuthRevokeSessionMutation>(revokeSessionMutation);
  const requestSubjectDeletion = createRelayMutation<AuthRequestSubjectDeletionMutation>(requestSubjectDeletionMutation);
  const beginPasskeyRegistration = createRelayEphemeralMutation<AuthBeginPasskeyRegistrationMutation>(
    defaultApiBaseUrl(), beginPasskeyRegistrationMutation,
  );
  const finishPasskeyRegistration = createRelayEphemeralMutation<AuthFinishPasskeyRegistrationMutation>(
    defaultApiBaseUrl(), finishPasskeyRegistrationMutation,
  );
  const [sessions, setSessions] = createSignal<OwnedSession[]>([]);
  const [revokingSession, setRevokingSession] = createSignal<string>();
  const [busy, setBusy] = createSignal<"passkey" | "delete" | "signout">();
  const [deletionRequested, setDeletionRequested] = createSignal(false);
  const unavailable = !passkeyAvailable();
  let active = true;

  onSettled(() => {
    const controller = new AbortController();
    void readOwnedSessions(authEpoch.environment(), controller.signal).then(
      (value) => setSessions(value.filter((session) => !session.revokedAt && session.id !== props.auth.currentSession.id)),
      (error: unknown) => {
        if (!controller.signal.aborted) app.showToast(errorMessage(error), "error");
      },
    );
    return () => {
      active = false;
      controller.abort();
    };
  });

  const revokeOtherSession = async (id: string) => {
    setRevokingSession(id);
    try {
      const result = await revokeSession({ input: { id } });
      if (result.revokeSession.errors.length || !result.revokeSession.session?.revokedAt) {
        throw new Error(result.revokeSession.errors[0]?.message ?? "세션을 해지하지 못했습니다.");
      }
      setSessions((current) => current.filter((session) => session.id !== id));
      app.showToast("다른 세션을 해지했습니다.");
    } catch (error) {
      app.showToast(errorMessage(error), "error");
    } finally {
      setRevokingSession(undefined);
    }
  };

  const registerPasskey = async () => {
    setBusy("passkey");
    try {
      const begin = await beginPasskeyRegistration({});
      if (!active) return;
      if (begin.beginPasskeyRegistration.errors.length || !begin.beginPasskeyRegistration.options) {
        throw new Error(begin.beginPasskeyRegistration.errors[0]?.message ?? "Passkey 등록을 시작하지 못했습니다.");
      }
      const options = begin.beginPasskeyRegistration.options;
      const credential = await createPasskey(JSON.parse(options.publicKeyJSON));
      if (!active) return;
      const finish = await finishPasskeyRegistration({ input: {
        ceremonyID: options.ceremonyID,
        credentialJSON: JSON.stringify(credential),
        label: "이 기기",
      } });
      if (!active) return;
      if (finish.finishPasskeyRegistration.errors.length || !finish.finishPasskeyRegistration.credential) {
        throw new Error(finish.finishPasskeyRegistration.errors[0]?.message ?? "Passkey 등록을 완료하지 못했습니다.");
      }
      app.showToast("이 기기에 Passkey를 등록했습니다.");
    } catch (error) {
      if (active) app.showToast(errorMessage(error), "error");
    } finally {
      if (active) setBusy(undefined);
    }
  };

  const requestDeletion = async () => {
    const subject = owner.subjects().find((candidate) => candidate.handle === app.currentSubject());
    if (
      !subject ||
      !confirm(`@${subject.handle} 잔디밭과 소유 데이터를 삭제 요청할까요? 요청은 비동기로 처리되며 완료 후 복구할 수 없습니다.`)
    ) return;
    setBusy("delete");
    try {
      const result = await requestSubjectDeletion({ input: { subjectID: subject.id } });
      if (result.requestSubjectDeletion.errors.length || !result.requestSubjectDeletion.request) {
        throw new Error(result.requestSubjectDeletion.errors[0]?.message ?? "삭제 요청을 접수하지 못했습니다.");
      }
      setDeletionRequested(true);
      app.showToast("잔디밭 삭제 요청을 접수했습니다.");
    } catch (error) {
      app.showToast(errorMessage(error), "error");
    } finally {
      setBusy(undefined);
    }
  };

  const signOut = async () => {
    setBusy("signout");
    try {
      const result = await signOutMutationCommit({});
      if (result.signOut.errors.length || !result.signOut.session) {
        throw new Error(result.signOut.errors[0]?.message ?? "로그아웃하지 못했습니다.");
      }
      owner.setSubjects([]);
      app.clearOwnerSubject();
      if (authEpoch.signedOut("signed-out")) return;
      app.showToast(authNoticeMessages["signed-out"]);
      props.onSignedOut();
    } catch (error) {
      app.showToast(errorMessage(error), "error");
      setBusy(undefined);
    }
  };

  return (
    <div class="auth-card account-card">
      <p class="eyebrow">Your account</p>
      <h2>{props.auth.user.primaryEmail}</h2>
      <p>이 세션은 <span>{formatTime(props.auth.currentSession.expiresAt)}</span>까지 유효합니다.</p>
      <dl class="account-meta">
        <div><dt>이메일 상태</dt><dd>{props.auth.user.emailVerifiedAt ? "확인됨" : "확인 대기"}</dd></div>
        <div><dt>계정 상태</dt><dd>{props.auth.user.status}</dd></div>
      </dl>
      <Show
        when={owner.subjects().length > 0}
        fallback={<OwnerSubjectCreator />}
      >
        <OwnerSubjectSelect />
      </Show>
      <a class="primary-button wide" href="/connections">Provider 관리 <Icon name="arrow" /></a>
      <button
        class="secondary-button wide"
        type="button"
        disabled={unavailable || Boolean(busy())}
        onClick={registerPasskey}
      >
        {busy() === "passkey" ? "등록 준비 중…" : "이 기기에 Passkey 등록"}
      </button>
      <Show when={app.currentSubject()}>
        <button
          class="danger-button wide"
          type="button"
          disabled={Boolean(busy()) || deletionRequested()}
          onClick={requestDeletion}
        >
          {deletionRequested()
            ? "삭제 요청됨"
            : busy() === "delete"
              ? "삭제 요청 중…"
              : "선택한 잔디밭 삭제 요청"}
        </button>
      </Show>
      <button class="text-button wide" type="button" disabled={Boolean(busy())} onClick={signOut}>
        {busy() === "signout" ? "로그아웃 중…" : "로그아웃"}
      </button>
      <Show when={sessions().length > 0}>
        <div class="account-sessions">
          <h3>다른 로그인 세션</h3>
          <ul>{sessions().map((session) => <li>
            <span>{formatTime(session.createdAt)}</span>
            <button class="text-button" type="button" disabled={Boolean(revokingSession())}
              onClick={() => void revokeOtherSession(session.id)}>다른 세션 해지</button>
          </li>)}</ul>
        </div>
      </Show>
    </div>
  );
}

export function AuthPage() {
  const app = useAppState();
  const authEpoch = useAuthEpoch();
  const [subjects, setSubjects] = createSignal<readonly SubjectDto[]>([]);
  const beginPasskeySignIn = createRelayEphemeralMutation<AuthBeginPasskeySignInMutation>(
    defaultApiBaseUrl(), beginPasskeySignInMutation,
  );
  const finishPasskeySignIn = createRelayEphemeralMutation<AuthFinishPasskeySignInMutation>(
    defaultApiBaseUrl(), finishPasskeySignInMutation,
  );
  const [auth, setAuth] = createSignal<AuthState>({ kind: "loading" });
  const [passkeyBusy, setPasskeyBusy] = createSignal(false);
  let attempt = 0;
  let active = true;

  const load = async () => {
    const currentAttempt = ++attempt;
    setAuth({ kind: "loading" });
    const token = takePendingMagicLinkToken() ?? magicLinkTokenFromUrl(location.href);
    if (magicLinkLocationHasToken(location.href)) {
      history.replaceState(null, "", magicLinkSafeLocation(location.href));
    }
    try {
      if (token) {
        const result = await api.consumeMagicLink(token);
        if (currentAttempt !== attempt) return;
        authEpoch.authenticated(result.user.id, true, "magic");
        return;
      }
      const result = await readCurrentViewer(authEpoch.environment());
      if (currentAttempt !== attempt) return;
      if (authEpoch.authenticated(result.user.id)) return;
      const subjects = await readOwnedSubjects(authEpoch.environment());
      if (currentAttempt !== attempt) return;
      setSubjects(subjects);
      const selected = preferredOwnedSubject(subjects, app.currentSubject());
      if (selected) app.selectOwnerSubject(selected);
      else app.clearOwnerSubject();
      setAuth({ kind: "signed-in", auth: result });
      showPendingAuthNotice(authEpoch.takeNotice(), (message) => app.showToast(message));
    } catch (error) {
      if (currentAttempt !== attempt) return;
      if ((error instanceof SignedOutError || error instanceof ApiError && error.status === 401) && !token) {
        if (authEpoch.signedOut()) return;
        setAuth({ kind: "signed-out" });
        showPendingAuthNotice(authEpoch.takeNotice(), (message) => app.showToast(message));
      } else {
        setAuth({ kind: "error", error });
      }
    }
  };

  const passkeySignIn = async () => {
    setPasskeyBusy(true);
    try {
      const begin = await beginPasskeySignIn({});
      if (!active) return;
      if (begin.beginPasskeySignIn.errors.length || !begin.beginPasskeySignIn.options) {
        throw new Error(begin.beginPasskeySignIn.errors[0]?.message ?? "Passkey 로그인을 시작하지 못했습니다.");
      }
      const options = begin.beginPasskeySignIn.options;
      const credential = await getPasskey(JSON.parse(options.publicKeyJSON));
      if (!active) return;
      const finish = await finishPasskeySignIn({ input: {
        ceremonyID: options.ceremonyID, credentialJSON: JSON.stringify(credential),
      } });
      if (!active) return;
      if (finish.finishPasskeySignIn.errors.length || !finish.finishPasskeySignIn.session) {
        throw new Error(finish.finishPasskeySignIn.errors[0]?.message ?? "Passkey 로그인을 완료하지 못했습니다.");
      }
      authEpoch.beginAuthenticatedSession("passkey");
    } catch (error) {
      if (active) app.showToast(errorMessage(error), "error");
    } finally {
      if (active) setPasskeyBusy(false);
    }
  };

  onSettled(() => {
    void load();
    return () => {
      active = false;
      attempt += 1;
    };
  });

  return <OwnerSubjectsContext value={{ subjects, setSubjects }}>
    <Switch>
      <Match when={auth().kind === "loading"}>
        <section class="page section-shell narrow">
          <div class="heatmap-loading" role="status"><p>로그인 상태를 확인하는 중…</p></div>
        </section>
      </Match>
      <Match when={auth().kind === "signed-out"}>
        <section class="auth-page">
          <AuthStory authenticated={false} />
          <div class="auth-panel">
            <SignedOutCard onPasskeySignIn={passkeySignIn} busy={passkeyBusy()} />
          </div>
        </section>
      </Match>
      <Match when={auth().kind === "signed-in"}>
        <section class="auth-page">
          <AuthStory authenticated />
          <div class="auth-panel">
            <SignedInCard
              auth={(auth() as Extract<AuthState, { kind: "signed-in" }>).auth}
              onSignedOut={() => setAuth({ kind: "signed-out" })}
            />
          </div>
        </section>
      </Match>
      <Match when={auth().kind === "error"}>
        <section class="auth-page">
          <AuthStory authenticated={false} />
          <div class="auth-panel">
            <SignedOutCard onPasskeySignIn={passkeySignIn} busy={passkeyBusy()} />
          </div>
          <div class="auth-overlay-error">
            <ErrorCallout
              error={(auth() as Extract<AuthState, { kind: "error" }>).error}
              retry={() => void load()}
            />
          </div>
        </section>
      </Match>
    </Switch>
  </OwnerSubjectsContext>;
}
