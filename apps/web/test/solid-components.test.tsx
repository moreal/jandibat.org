import type {
  ActivityTimelineResponseDto,
  CustomProviderDto,
  ProviderCatalogItemDto,
} from "@jandibat/contracts";
import { cleanup, fireEvent, render } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppStateProvider } from "../src/app/state";
import { Heatmap } from "../src/heatmap/Heatmap";
import { CustomProviderCards } from "../src/providers/CustomProviderCards";
import { ProviderCards } from "../src/providers/ProviderCards";

afterEach(() => {
  cleanup();
  localStorage.clear();
});

const attack = `<img src=x onerror="alert(1)">\" autofocus onfocus="alert(2)`;

function withState(view: () => unknown) {
  return render(() => <AppStateProvider>{view() as never}</AppStateProvider>);
}

describe("Solid component trust boundaries", () => {
  it("renders heatmap API strings as text and safe attributes", () => {
    const timeline: ActivityTimelineResponseDto = {
      subject: attack,
      timezone: attack,
      from: "2025-08-13",
      to: "2026-08-12",
      generatedAt: "2026-08-12T00:00:00Z",
      stale: false,
      environments: [{
        id: "environment-1",
        key: "malicious",
        name: attack,
        scope: "global",
        metadata: {},
      }],
      days: [{
        date: "2026-08-12",
        count: 1,
        level: 4,
        entries: [{
          environmentId: "environment-1",
          action: attack,
          metric: { name: attack, value: 1 },
          metadata: {},
        }],
      }],
    };

    const { container } = render(() => <Heatmap timeline={timeline} />);

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("[autofocus]")).toBeNull();
    expect(container.textContent).toContain(attack);
    const cell = container.querySelector<HTMLButtonElement>('[data-date="2026-08-12"]');
    expect(cell?.getAttribute("aria-label")).toContain(attack);
    expect(container.querySelectorAll(".heatmap-month")).toHaveLength(53);
  });

  it("renders provider data as text and requires explicit private-data consent", async () => {
    const provider: ProviderCatalogItemDto = {
      id: "github",
      name: attack,
      category: "git-hosting",
      authMethods: ["oauth2", "token", "none"],
      supportsPrivateData: true,
      supportsScheduledSync: true,
    };
    const { container } = withState(() => (
      <ProviderCards subject="garden" catalog={[provider]} connections={[]} reload={vi.fn()} />
    ));

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("[autofocus]")).toBeNull();
    expect(container.textContent).toContain(attack);
    const consent = container.querySelector<HTMLInputElement>('[name="includePrivate"]');
    expect(consent?.required).toBe(true);
    expect(consent?.checked).toBe(false);

    const method = container.querySelector<HTMLSelectElement>('[name="authMethod"]');
    expect(method).not.toBeNull();
    await fireEvent.change(method!, { target: { value: "none" } });
    expect(consent?.required).toBe(false);
    expect(consent?.closest<HTMLElement>(".private-consent-field")?.hidden).toBe(true);
  });

  it("renders custom-provider fields as text", () => {
    const provider: CustomProviderDto = {
      id: attack,
      subjectId: "subject-1",
      environmentId: "environment-1",
      name: attack,
      key: attack,
      description: attack,
      status: "active",
      allowedActions: [attack],
      lastIngestedAt: null,
      createdAt: "2026-08-12T00:00:00Z",
      updatedAt: "2026-08-12T00:00:00Z",
    };
    const { container } = withState(() => (
      <CustomProviderCards
        subject="garden"
        providers={[provider]}
        reload={vi.fn()}
        select={vi.fn()}
        showSecret={vi.fn()}
      />
    ));

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("[autofocus]")).toBeNull();
    expect(container.textContent).toContain(attack);
  });
});
