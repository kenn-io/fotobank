import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import HiddenLockStrip from "./HiddenLockStrip.svelte";
import type { HiddenStore } from "../hidden/hiddenStore.svelte";

function makeStore(overrides: Partial<HiddenStore> = {}): HiddenStore {
  return {
    configured: true,
    unlocked: true,
    expiresAt: null,
    lockedOutUntil: null,
    error: null,
    refresh: vi.fn(),
    unlock: vi.fn(),
    lock: vi.fn().mockResolvedValue(undefined),
    hide: vi.fn(),
    unhide: vi.fn(),
    ...overrides,
  } as unknown as HiddenStore;
}

describe("HiddenLockStrip", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders countdown given a future expiresAt", () => {
    // expiresAt is 90 seconds from now
    const now = Date.now();
    const expiresAt = new Date(now + 90_000).toISOString();
    const store = makeStore({ expiresAt });
    const { container } = render(HiddenLockStrip, { props: { hiddenStore: store } });
    // Should show something like "1:30" or "1m 30s"
    expect(container.textContent).toMatch(/\d/);
  });

  it("renders a Lock now button", () => {
    const now = Date.now();
    const expiresAt = new Date(now + 300_000).toISOString();
    const store = makeStore({ expiresAt });
    const { container } = render(HiddenLockStrip, { props: { hiddenStore: store } });
    const btn = container.querySelector("button");
    expect(btn).not.toBeNull();
    expect(btn?.textContent?.trim()).toBe("Lock now");
  });

  it("calls hiddenStore.lock() when Lock now is clicked", async () => {
    const lockMock = vi.fn().mockResolvedValue(undefined);
    const now = Date.now();
    const expiresAt = new Date(now + 300_000).toISOString();
    const store = makeStore({ expiresAt, lock: lockMock as never });
    const { container } = render(HiddenLockStrip, { props: { hiddenStore: store } });
    const btn = container.querySelector("button") as HTMLButtonElement;
    await fireEvent.click(btn);
    expect(lockMock).toHaveBeenCalledOnce();
  });

  it("countdown ticks each second", async () => {
    const now = Date.now();
    // 65 seconds → initially "1:05"
    const expiresAt = new Date(now + 65_000).toISOString();
    const store = makeStore({ expiresAt });
    const { container } = render(HiddenLockStrip, { props: { hiddenStore: store } });

    const initialText = container.textContent ?? "";
    expect(initialText).toContain("1:05");

    // Advance 1 second
    await vi.advanceTimersByTimeAsync(1000);
    const afterOneSecond = container.textContent ?? "";
    expect(afterOneSecond).toContain("1:04");
  });

  it("shows 0:00 when expiresAt is in the past", () => {
    const past = new Date(Date.now() - 5_000).toISOString();
    const store = makeStore({ expiresAt: past });
    const { container } = render(HiddenLockStrip, { props: { hiddenStore: store } });
    expect(container.textContent).toContain("0:00");
  });

  it("shows 0:00 when expiresAt is null", () => {
    const store = makeStore({ expiresAt: null });
    const { container } = render(HiddenLockStrip, { props: { hiddenStore: store } });
    expect(container.textContent).toContain("0:00");
  });

  it("auto-locks when countdown ticks to 0:00 (finding #7)", async () => {
    const lockMock = vi.fn().mockResolvedValue(undefined);
    const now = Date.now();
    // expires in 1 second
    const expiresAt = new Date(now + 1_000).toISOString();
    const store = makeStore({ expiresAt, lock: lockMock as never });
    render(HiddenLockStrip, { props: { hiddenStore: store } });

    // Advance past expiry — interval fires and remaining becomes 0
    await vi.advanceTimersByTimeAsync(2000);
    expect(lockMock).toHaveBeenCalledOnce();
  });
});
