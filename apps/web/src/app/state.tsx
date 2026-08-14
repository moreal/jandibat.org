import type { SubjectDto } from "@jandibat/contracts";
import {
  createContext,
  createSignal,
  onSettled,
  useContext,
  type Accessor,
  type ParentProps,
  type Setter,
} from "solid-js";

const OWNER_SUBJECT_STORAGE_KEY = "jandibat:owner-subject";
const EXPLORE_SUBJECT_STORAGE_KEY = "jandibat:explore-subject";

export type ToastTone = "success" | "error";

export type ToastMessage = {
  id: number;
  message: string;
  tone: ToastTone;
};

export type AppState = {
  currentSubject: Accessor<string>;
  exploreSubject: Accessor<string>;
  ownedSubjects: Accessor<readonly SubjectDto[]>;
  toast: Accessor<ToastMessage | undefined>;
  selectOwnerSubject(subject: SubjectDto): void;
  clearOwnerSubject(): void;
  setExploreSubject(subject: string): void;
  setOwnedSubjects: Setter<readonly SubjectDto[]>;
  showToast(message: string, tone?: ToastTone): void;
};

const AppStateContext = createContext<AppState>();

function persistedValue(key: string): string {
  return localStorage.getItem(key)?.trim() ?? "";
}

function createAppState(): AppState {
  const [currentSubject, setCurrentSubject] = createSignal(
    persistedValue(OWNER_SUBJECT_STORAGE_KEY),
  );
  const [exploreSubject, setExploreSubjectSignal] = createSignal(
    persistedValue(EXPLORE_SUBJECT_STORAGE_KEY),
  );
  const [ownedSubjects, setOwnedSubjects] = createSignal<readonly SubjectDto[]>([]);
  const [toast, setToast] = createSignal<ToastMessage>();
  let toastSequence = 0;
  let toastTimer: number | undefined;

  onSettled(() => () => {
    if (toastTimer !== undefined) window.clearTimeout(toastTimer);
  });

  return {
    currentSubject,
    exploreSubject,
    ownedSubjects,
    toast,
    selectOwnerSubject(subject) {
      setCurrentSubject(subject.handle);
      localStorage.setItem(OWNER_SUBJECT_STORAGE_KEY, subject.handle);
    },
    clearOwnerSubject() {
      setCurrentSubject("");
      localStorage.removeItem(OWNER_SUBJECT_STORAGE_KEY);
    },
    setExploreSubject(subject) {
      const normalized = subject.trim();
      setExploreSubjectSignal(normalized);
      if (normalized) localStorage.setItem(EXPLORE_SUBJECT_STORAGE_KEY, normalized);
      else localStorage.removeItem(EXPLORE_SUBJECT_STORAGE_KEY);
    },
    setOwnedSubjects,
    showToast(message, tone = "success") {
      if (toastTimer !== undefined) window.clearTimeout(toastTimer);
      setToast({ id: ++toastSequence, message, tone });
      const duration = Math.max(4200, Math.min(8000, message.length * 95));
      toastTimer = window.setTimeout(() => {
        setToast(undefined);
        toastTimer = undefined;
      }, duration);
    },
  };
}

export function AppStateProvider(props: ParentProps) {
  return <AppStateContext value={createAppState()}>{props.children}</AppStateContext>;
}

export function useAppState(): AppState {
  return useContext(AppStateContext);
}
