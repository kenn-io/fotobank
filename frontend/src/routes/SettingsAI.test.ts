import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/svelte";
import { flushSync, tick } from "svelte";
import SettingsAI from "./SettingsAI.svelte";
import { aiHealthStore } from "../lib/ai/health.svelte";
import { AIInspectionStore } from "../lib/ai/inspectionStore.svelte";
import * as client from "../lib/ai/client";
import type { AIHealth } from "../lib/ai/types";

vi.mock("../lib/ai/client", () => ({
  acknowledgeHiddenProcessing: vi.fn().mockResolvedValue(undefined),
  backfillAI: vi.fn().mockResolvedValue({ enqueued: 5 }),
  listAIFailures: vi.fn().mockResolvedValue([]),
  retryFailedAI: vi.fn().mockResolvedValue({ enqueued: 3 }),
}));

const base: AIHealth = {
  enabled: true,
  paused_reason: "",
  vision: { reachable: true, last_check_at: "2026-04-30T18:42:11Z" },
  tag: {
    active_fingerprint: "fp-tag",
    pending: 3,
    working: 1,
    blocked: 0,
    failed_active: 0,
    skipped: 0,
    done: 100,
    throughput_per_min: 0,
  },
  caption: {
    active_fingerprint: "fp-cap",
    pending: 0,
    working: 0,
    blocked: 0,
    failed_active: 0,
    skipped: 0,
    done: 50,
    throughput_per_min: 0,
  },
};

beforeEach(() => {
  vi.spyOn(aiHealthStore, "refresh").mockResolvedValue(undefined);
  aiHealthStore.health = null;
  vi.mocked(client.listAIFailures).mockResolvedValue([]);
});

afterEach(() => vi.restoreAllMocks());

describe("SettingsAI", () => {
  it("shows ack modal when paused on acknowledgement", async () => {
    aiHealthStore.health = { ...base, paused_reason: "acknowledgement_required" };
    render(SettingsAI);
    await waitFor(() => expect(screen.getByText(/Before AI starts processing/)).toBeTruthy());
  });

  it("hides ack modal once acknowledged and renders task cards", async () => {
    aiHealthStore.health = { ...base };
    render(SettingsAI);
    await waitFor(() => expect(screen.queryByText(/Before AI starts/)).toBeNull());
    expect(screen.getByText("Tag")).toBeTruthy();
    expect(screen.getByText("Caption")).toBeTruthy();
  });

  it("renders task counters", async () => {
    aiHealthStore.health = { ...base };
    render(SettingsAI);
    await waitFor(() => expect(screen.getByText("100")).toBeTruthy()); // tag done
    expect(screen.getByText("50")).toBeTruthy(); // caption done
  });

  it("renders 'AI is disabled' when paused_reason=config_disabled", async () => {
    aiHealthStore.health = { ...base, paused_reason: "config_disabled" };
    render(SettingsAI);
    await waitFor(() => expect(screen.getByText(/AI is disabled in config\.toml/)).toBeTruthy());
  });

  it("toggling AI Inspection persists ai.inspection=true", async () => {
    // The toggle calls inspectionStore.set(true) which PUTs the JSON
    // boolean `true` to /api/v1/settings/user/ai.inspection. The test
    // injects a stubbed client so the underlying PUT is observable;
    // checking the post-click state of the store covers both the
    // optimistic local update and the persistence call.
    aiHealthStore.health = { ...base };
    const PUT = vi.fn().mockResolvedValue({ error: undefined });
    const GET = vi.fn().mockResolvedValue({ data: undefined, error: undefined });
    const store = new AIInspectionStore({ GET, PUT } as never);
    render(SettingsAI, { props: { inspectionStore: store } });
    flushSync();
    await tick();
    const toggle = screen.getByTestId("ai-inspection-toggle") as HTMLInputElement;
    expect(toggle.checked).toBe(false);
    await fireEvent.click(toggle);
    // The local store reflects the new value optimistically; the PUT
    // is observable on the mocked client. Together these confirm the
    // toggle persists ai.inspection=true via the user-settings route.
    expect(store.enabled).toBe(true);
    expect(PUT).toHaveBeenCalledWith(
      "/api/v1/settings/user/{key}",
      expect.objectContaining({
        params: { path: { key: "ai.inspection" } },
        body: { value: "true" },
      }),
    );
  });
});
