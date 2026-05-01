import { fireEvent, render } from "@testing-library/svelte";
import { describe, it, expect, beforeEach, vi } from "vitest";
import AIStatusDot from "./AIStatusDot.svelte";
import { aiHealthStore } from "../ai/health.svelte";
import { router } from "../router/router.svelte";
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

  it("plain-click routes through SPA navigate (no full reload)", async () => {
    aiHealthStore.health = healthy;
    const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
    try {
      const { container } = render(AIStatusDot);
      const link = container.querySelector("a.ai-dot");
      expect(link).toBeTruthy();
      // fireEvent.click defaults to a plain-button, no-modifier event,
      // which is what handleInternalLinkClick treats as SPA nav. Without
      // the onclick wiring the browser would do a full page reload.
      await fireEvent.click(link!);
      expect(navigate).toHaveBeenCalledWith("/settings/ai");
    } finally {
      navigate.mockRestore();
    }
  });
});
