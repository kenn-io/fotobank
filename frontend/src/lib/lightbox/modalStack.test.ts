import { describe, it, expect, vi } from "vitest";
import { ModalStack } from "./modalStack.svelte";

describe("ModalStack", () => {
  it("starts empty", () => {
    const s = new ModalStack();
    expect(s.top()).toBeNull();
    expect(s.dispatchEscape()).toBe(false);
  });

  it("push/pop/isTopmost", () => {
    const s = new ModalStack();
    const onA = vi.fn();
    const onB = vi.fn();
    s.push({ id: "a", onEscape: onA });
    s.push({ id: "b", onEscape: onB });
    expect(s.isTopmost("a")).toBe(false);
    expect(s.isTopmost("b")).toBe(true);
    s.pop("b");
    expect(s.isTopmost("a")).toBe(true);
    s.pop("a");
    expect(s.top()).toBeNull();
  });

  it("dispatchEscape calls topmost handler and returns true; does NOT auto-pop", () => {
    const s = new ModalStack();
    const onA = vi.fn();
    const onB = vi.fn();
    s.push({ id: "a", onEscape: onA });
    s.push({ id: "b", onEscape: onB });
    expect(s.dispatchEscape()).toBe(true);
    expect(onB).toHaveBeenCalledOnce();
    expect(onA).not.toHaveBeenCalled();
    // Stack still has both — pop happens via the modal's own unmount.
    expect(s.isTopmost("b")).toBe(true);
  });

  it("dispatchEscape returns false on empty stack", () => {
    const s = new ModalStack();
    expect(s.dispatchEscape()).toBe(false);
  });

  it("idempotent pop of unknown id", () => {
    const s = new ModalStack();
    s.push({ id: "a", onEscape: vi.fn() });
    s.pop("nope");
    s.pop("a");
    s.pop("a");
    expect(s.top()).toBeNull();
  });

  it("pause/resume of trap on push/pop", () => {
    const s = new ModalStack();
    const trapA = { pause: vi.fn(), resume: vi.fn(), release: vi.fn() };
    const trapB = { pause: vi.fn(), resume: vi.fn(), release: vi.fn() };
    s.push({ id: "a", onEscape: vi.fn(), trap: trapA });
    expect(trapA.pause).not.toHaveBeenCalled();
    s.push({ id: "b", onEscape: vi.fn(), trap: trapB });
    expect(trapA.pause).toHaveBeenCalledOnce();
    expect(trapB.pause).not.toHaveBeenCalled();
    s.pop("b");
    expect(trapA.resume).toHaveBeenCalledOnce();
  });

  it("does NOT resume trap when a non-topmost entry is popped", () => {
    const s = new ModalStack();
    const trapA = { pause: vi.fn(), resume: vi.fn(), release: vi.fn() };
    const trapB = { pause: vi.fn(), resume: vi.fn(), release: vi.fn() };
    s.push({ id: "a", onEscape: vi.fn(), trap: trapA });
    s.push({ id: "b", onEscape: vi.fn(), trap: trapB });
    // Out-of-order unmount: bottom pops while top is still mounted.
    // Top's trap is already active, so resume() must NOT fire.
    s.pop("a");
    expect(trapB.resume).not.toHaveBeenCalled();
  });

  it("rapid Esc only invokes the top handler once until that entry pops", () => {
    const s = new ModalStack();
    const onA = vi.fn();
    s.push({ id: "a", onEscape: onA });
    expect(s.dispatchEscape()).toBe(true);
    expect(s.dispatchEscape()).toBe(true);
    expect(s.dispatchEscape()).toBe(true);
    expect(onA).toHaveBeenCalledTimes(1);
    // After unmount/pop and re-push (a fresh modal), Esc fires again.
    s.pop("a");
    s.push({ id: "a", onEscape: onA });
    s.dispatchEscape();
    expect(onA).toHaveBeenCalledTimes(2);
  });

  it("onEscape returning false leaves the entry escapable for a later dispatch", () => {
    const s = new ModalStack();
    let pending = true;
    const onA = vi.fn(() => {
      if (pending) return false;
      // accepted: a real modal would unmount + pop here.
    });
    s.push({ id: "a", onEscape: onA });
    // First Esc while pending — handler runs but rejects.
    expect(s.dispatchEscape()).toBe(true);
    expect(onA).toHaveBeenCalledTimes(1);
    // Second Esc still pending — must run again, not be suppressed.
    expect(s.dispatchEscape()).toBe(true);
    expect(onA).toHaveBeenCalledTimes(2);
    // Operation finishes; Esc now accepted, handler runs once more.
    pending = false;
    expect(s.dispatchEscape()).toBe(true);
    expect(onA).toHaveBeenCalledTimes(3);
    // After the accepted dispatch the entry is marked closing, so a
    // subsequent Esc before unmount/pop does NOT re-invoke.
    expect(s.dispatchEscape()).toBe(true);
    expect(onA).toHaveBeenCalledTimes(3);
  });
});
