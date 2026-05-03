import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import AppHeader from "./AppHeader.svelte";
import { router } from "../router/router.svelte";

// Reset URL between tests so each case starts at a known route.
function setLocation(pathname: string) {
  window.history.replaceState({}, "", pathname);
  router.syncFromLocation();
}

// Default props mirror what App.svelte threads from AppConfigStore.
// Tests that need a different identity override individual fields.
// Centralizing the defaults here means a future prop addition only
// touches this helper, not every render() call.
function renderHeader(overrides: Record<string, unknown> = {}) {
  return render(AppHeader, {
    props: {
      hub: "dev-local",
      handle: "owner",
      ...overrides,
    },
  });
}

describe("AppHeader search input", () => {
  beforeEach(() => {
    setLocation("/");
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("debounces input by ~300ms before committing", async () => {
    vi.useFakeTimers();
    const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
    const { container } = renderHeader();
    const input = container.querySelector("input.search") as HTMLInputElement;
    expect(input).toBeTruthy();

    // Three keystrokes within the debounce window: only the final one
    // should commit, and only after the debounce elapses.
    await fireEvent.input(input, { target: { value: "d" } });
    await fireEvent.input(input, { target: { value: "do" } });
    await fireEvent.input(input, { target: { value: "dog" } });

    // No commit yet — timer hasn't elapsed.
    expect(navigate).not.toHaveBeenCalled();

    // Advance just shy of the debounce — still no commit.
    await vi.advanceTimersByTimeAsync(299);
    expect(navigate).not.toHaveBeenCalled();

    // Cross the threshold — exactly one commit fires with the final value.
    await vi.advanceTimersByTimeAsync(1);
    expect(navigate).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith("/search?q=dog");
  });

  it("Enter flushes the pending debounce and pushes a real history entry", async () => {
    vi.useFakeTimers();
    const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
    const { container } = renderHeader();
    const input = container.querySelector("input.search") as HTMLInputElement;

    await fireEvent.input(input, { target: { value: "trees" } });
    // Debounce timer is armed but not fired.
    expect(navigate).not.toHaveBeenCalled();

    await fireEvent.keyDown(input, { key: "Enter" });

    // Enter commits immediately — push (no replace option).
    expect(navigate).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith("/search?q=trees");

    // Advance past the debounce — the pending timer must have been
    // cleared by Enter, so no second commit fires.
    await vi.advanceTimersByTimeAsync(500);
    expect(navigate).toHaveBeenCalledTimes(1);
  });

  it("typing while on /search uses replace-state navigation", async () => {
    vi.useFakeTimers();
    setLocation("/search");
    const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
    const { container } = renderHeader();
    const input = container.querySelector("input.search") as HTMLInputElement;

    await fireEvent.input(input, { target: { value: "cat" } });
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);

    expect(navigate).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith("/search?q=cat", { replace: true });
  });

  it("seeds the input from ?q= when mounted on /search", () => {
    setLocation("/search?q=hello");
    const { container } = renderHeader();
    const input = container.querySelector("input.search") as HTMLInputElement;
    expect(input.value).toBe("hello");
  });

  it("cancels a pending debounce when the route changes away from /search", async () => {
    // User types "do" on /library, then a sidebar click navigates them
    // to /albums before the 300ms debounce fires. The stale timer must
    // be cancelled — otherwise it would navigate back to /search?q=do
    // and stomp the user's chosen destination.
    vi.useFakeTimers();
    const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
    const { container } = renderHeader();
    const input = container.querySelector("input.search") as HTMLInputElement;

    await fireEvent.input(input, { target: { value: "do" } });
    expect(navigate).not.toHaveBeenCalled();

    // Simulate external navigation: location changes and router.current
    // is synced. The header's effect on router.current must cancel the
    // pending timer.
    setLocation("/albums");

    // Advance well past the debounce window — no commit may fire.
    await vi.advanceTimersByTimeAsync(500);
    expect(navigate).not.toHaveBeenCalled();
  });

  it("⌘K focuses the search input from anywhere", async () => {
    const { container } = renderHeader();
    const input = container.querySelector("input.search") as HTMLInputElement;
    expect(input).toBeTruthy();
    // Move focus elsewhere first so we can prove ⌘K shifts it back. The
    // brand div isn't focusable; the search input itself is what we want
    // to assert focus on, so steal focus to document.body explicitly.
    (document.activeElement as HTMLElement | null)?.blur();
    expect(document.activeElement).toBe(document.body);

    // jsdom dispatches keydown on window for plain `new KeyboardEvent`,
    // which is what AppHeader listens on. metaKey emulates ⌘K on macOS.
    const ev = new KeyboardEvent("keydown", { key: "k", metaKey: true, cancelable: true });
    window.dispatchEvent(ev);

    expect(document.activeElement).toBe(input);
    expect(ev.defaultPrevented).toBe(true);
  });
});

describe("AppHeader identity display", () => {
  beforeEach(() => {
    setLocation("/");
  });

  it("renders {hub}: {handle} from the principal props", () => {
    const { getByTestId } = renderHeader({ hub: "dev-local", handle: "owner" });
    expect(getByTestId("app-identity").textContent).toBe("dev-local: owner");
  });

  it("renders em-dash placeholders before /me has resolved", () => {
    const { getByTestId } = renderHeader({ hub: undefined, handle: undefined });
    expect(getByTestId("app-identity").textContent).toBe("—: —");
  });
});

// Mirror AppHeader.svelte's debounce constant. Kept inline so the
// test fails loudly if the constant drifts (the assertion above
// exercises the boundary at 299 / 300 ms).
const DEBOUNCE_MS = 300;
