import { render } from "@testing-library/svelte";
import { describe, it, expect, beforeEach } from "vitest";
import AIStatusDot from "./AIStatusDot.svelte";
import { aiHealthStore } from "../ai/health.svelte";
import type { AIHealth } from "../ai/types";

const healthy: AIHealth = {
  enabled: true,
  paused_reason: "",
  vision: { reachable: true, last_check_at: "2026-04-30T18:42:11Z" },
  tag: {
    active_fingerprint: "fp",
    pending: 0,
    working: 0,
    blocked: 0,
    failed_active: 0,
    skipped: 0,
    done: 0,
    throughput_per_min: 0,
  },
  caption: {
    active_fingerprint: "fp",
    pending: 0,
    working: 0,
    blocked: 0,
    failed_active: 0,
    skipped: 0,
    done: 0,
    throughput_per_min: 0,
  },
};

describe("AIStatusDot", () => {
  beforeEach(() => {
    aiHealthStore.health = null;
  });

  it("renders link with aria-label when state is idle", () => {
    aiHealthStore.health = healthy;
    const { container } = render(AIStatusDot);
    const link = container.querySelector("a.ai-dot");
    expect(link).toBeTruthy();
    expect(link?.getAttribute("aria-label")).toBe("AI healthy");
    expect(link?.getAttribute("data-state")).toBe("idle");
  });

  it("hides when health is null (state=hidden)", () => {
    aiHealthStore.health = null;
    const { container } = render(AIStatusDot);
    expect(container.querySelector("a.ai-dot")).toBeNull();
  });

  it("hides when paused_reason=config_disabled", () => {
    aiHealthStore.health = { ...healthy, paused_reason: "config_disabled" };
    const { container } = render(AIStatusDot);
    expect(container.querySelector("a.ai-dot")).toBeNull();
  });

  it("data-state=paused when ack required", () => {
    aiHealthStore.health = { ...healthy, paused_reason: "acknowledgement_required" };
    const { container } = render(AIStatusDot);
    const link = container.querySelector("a.ai-dot");
    expect(link?.getAttribute("data-state")).toBe("paused");
  });
});
