import { createContext, createSignal, onSettled, useContext, type Accessor } from "solid-js";
import type { Environment } from "relay-runtime";
import { createRelayEnvironment } from "./environment";

const AUTH_EPOCH_STORAGE_KEY = "jandibat:auth-epoch";

export type AuthEpoch = {
  environment: Accessor<Environment>;
  beginAuthenticatedSession(notice: AuthNotice): void;
  authenticated(userID: string, explicitSignIn?: boolean, notice?: AuthNotice): boolean;
  signedOut(notice?: AuthNotice): boolean;
  takeNotice(): AuthNotice | undefined;
};

export type AuthNotice = "magic" | "passkey" | "signed-out";

export const AuthEpochContext = createContext<AuthEpoch>();

/** An account transition must never reuse normalized records from a previous cookie. */
export function createAuthEpoch(apiBaseUrl: string): AuthEpoch {
  const [environment, setEnvironment] = createSignal(createRelayEnvironment(apiBaseUrl));
  let accountID: string | null | undefined;
  let awaitingIdentity = false;
  let pendingNotice: AuthNotice | undefined;

  const rotate = (broadcast: boolean) => {
    setEnvironment(createRelayEnvironment(apiBaseUrl));
    if (!broadcast) return;
    try {
      // The marker contains no account, session, cookie, or token data.
      localStorage.setItem(AUTH_EPOCH_STORAGE_KEY, crypto.randomUUID());
    } catch {
      // Storage can be disabled; local cache isolation must still succeed.
    }
  };

  const onStorage = (event: StorageEvent) => {
    if (event.key !== AUTH_EPOCH_STORAGE_KEY || !event.newValue) return;
    accountID = undefined;
    awaitingIdentity = false;
    pendingNotice = undefined;
    rotate(false);
  };
  window.addEventListener("storage", onStorage);
  onSettled(() => () => window.removeEventListener("storage", onStorage));

  return {
    environment,
    beginAuthenticatedSession(notice) {
      accountID = undefined;
      awaitingIdentity = true;
      pendingNotice = notice;
      rotate(true);
    },
    authenticated(userID, explicitSignIn = false, notice) {
      if (awaitingIdentity && !explicitSignIn) {
        awaitingIdentity = false;
        accountID = userID;
        return false;
      }
      awaitingIdentity = false;
      const changed = explicitSignIn || accountID !== userID;
      accountID = userID;
      if (changed) {
        pendingNotice = notice;
        rotate(true);
      }
      return changed;
    },
    signedOut(notice) {
      awaitingIdentity = false;
      const changed = accountID !== null;
      accountID = null;
      if (changed) {
        pendingNotice = notice;
        rotate(true);
      }
      return changed;
    },
    takeNotice() {
      const notice = pendingNotice;
      pendingNotice = undefined;
      return notice;
    },
  };
}

export function useAuthEpoch(): AuthEpoch {
  const epoch = useContext(AuthEpochContext);
  if (!epoch) throw new Error("AuthEpoch must be used inside the ready application.");
  return epoch;
}
