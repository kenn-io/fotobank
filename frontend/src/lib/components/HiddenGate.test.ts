import { describe, it, expect, vi } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/svelte";
import HiddenGate from "./HiddenGate.svelte";
import type { HiddenStore } from "../hidden/hiddenStore.svelte";

function makeStore(overrides: Partial<HiddenStore> = {}): HiddenStore {
  return {
    configured: true,
    unlocked: false,
    expiresAt: null,
    lockedOutUntil: null,
    error: null,
    refresh: vi.fn(),
    unlock: vi.fn().mockResolvedValue(undefined),
    lock: vi.fn(),
    hide: vi.fn(),
    unhide: vi.fn(),
    ...overrides,
  } as unknown as HiddenStore;
}

describe("HiddenGate", () => {
  it("renders CTA when not configured", () => {
    const store = makeStore({ configured: false });
    const { container } = render(HiddenGate, { props: { hiddenStore: store } });
    expect(container.textContent).toContain("fotobank hidden setup");
  });

  it("renders passcode form when configured and locked", () => {
    const store = makeStore({ configured: true, unlocked: false });
    const { container } = render(HiddenGate, { props: { hiddenStore: store } });
    const input = container.querySelector("input[type='password']");
    expect(input).not.toBeNull();
  });

  it("does not render passcode form when unlocked", () => {
    const store = makeStore({ configured: true, unlocked: true });
    const { container } = render(HiddenGate, { props: { hiddenStore: store } });
    const input = container.querySelector("input[type='password']");
    expect(input).toBeNull();
  });

  it("calls hiddenStore.unlock with passcode on submit", async () => {
    const unlockMock = vi.fn().mockResolvedValue(undefined);
    const store = makeStore({ configured: true, unlocked: false, unlock: unlockMock as never });
    const { container } = render(HiddenGate, { props: { hiddenStore: store } });
    const input = container.querySelector("input[type='password']") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "mypasscode" } });
    const form = container.querySelector("form");
    await fireEvent.submit(form!);
    await waitFor(() => {
      expect(unlockMock).toHaveBeenCalledWith("mypasscode");
    });
  });

  it("shows wrong_passcode error message on wrong_passcode error", () => {
    const store = makeStore({
      configured: true,
      unlocked: false,
      error: { kind: "wrong_passcode" },
    });
    const { container } = render(HiddenGate, { props: { hiddenStore: store } });
    expect(container.textContent).toContain("Passcode incorrect");
  });

  it("shows locked_out countdown message on locked_out error", () => {
    const store = makeStore({
      configured: true,
      unlocked: false,
      error: { kind: "locked_out", retryAfterSeconds: 300 },
    });
    const { container } = render(HiddenGate, { props: { hiddenStore: store } });
    expect(container.textContent).toContain("Too many attempts");
  });

  it("shows invalid_input error message on 400 error", () => {
    const store = makeStore({
      configured: true,
      unlocked: false,
      error: { kind: "invalid_input" },
    });
    const { container } = render(HiddenGate, { props: { hiddenStore: store } });
    expect(container.textContent).toContain("1–1024 bytes");
  });

  it("shows identity_required message on 401 error", () => {
    const store = makeStore({
      configured: true,
      unlocked: false,
      error: { kind: "identity_required" },
    });
    const { container } = render(HiddenGate, { props: { hiddenStore: store } });
    expect(container.textContent).toContain("Identity not configured");
  });

  it("shows network error message on network error", () => {
    const store = makeStore({
      configured: true,
      unlocked: false,
      error: { kind: "network", message: "fetch failed" },
    });
    const { container } = render(HiddenGate, { props: { hiddenStore: store } });
    expect(container.textContent).toContain("Could not reach server");
  });
});
