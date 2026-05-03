import { describe, it, expect, vi, afterEach } from "vitest";
import { fireEvent, render } from "@testing-library/svelte";
import SearchBar from "./SearchBar.svelte";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("SearchBar", () => {
  it("renders an input with the documented placeholder", () => {
    const { getByTestId } = render(SearchBar, {
      props: { onsubmit: () => {} },
    });
    const input = getByTestId("search-input") as HTMLInputElement;
    expect(input).toBeTruthy();
    expect(input.placeholder).toBe("Search photos, cameras, places, dates…");
  });

  it("⌘K (Meta+K) and Ctrl+K both focus the input", () => {
    // Render the component twice so each shortcut is verified against
    // a fresh component (and a fresh effect-bound listener). Anything
    // less and the second dispatch could be no-op'd by the first key
    // already having focused the input.
    const meta = render(SearchBar, { props: { onsubmit: () => {} } });
    const metaInput = meta.getByTestId("search-input") as HTMLInputElement;
    (document.activeElement as HTMLElement | null)?.blur();
    expect(document.activeElement).toBe(document.body);
    const metaEv = new KeyboardEvent("keydown", {
      key: "k",
      metaKey: true,
      cancelable: true,
    });
    window.dispatchEvent(metaEv);
    expect(document.activeElement).toBe(metaInput);
    expect(metaEv.defaultPrevented).toBe(true);
    meta.unmount();

    const ctrl = render(SearchBar, { props: { onsubmit: () => {} } });
    const ctrlInput = ctrl.getByTestId("search-input") as HTMLInputElement;
    (document.activeElement as HTMLElement | null)?.blur();
    expect(document.activeElement).toBe(document.body);
    const ctrlEv = new KeyboardEvent("keydown", {
      key: "k",
      ctrlKey: true,
      cancelable: true,
    });
    window.dispatchEvent(ctrlEv);
    expect(document.activeElement).toBe(ctrlInput);
    expect(ctrlEv.defaultPrevented).toBe(true);
    ctrl.unmount();
  });

  it("kbd hint hides on focus via the .focused class on the wrapper", async () => {
    const { container, getByTestId } = render(SearchBar, {
      props: { onsubmit: () => {} },
    });
    const input = getByTestId("search-input") as HTMLInputElement;
    const wrapper = container.querySelector(".search-bar") as HTMLElement;
    expect(wrapper).not.toBeNull();
    // The kbd hint is hidden via two rules: `:focus-within` (CSS-only,
    // production) and `.focused` (class toggle, exercised here).
    // jsdom doesn't compute :focus-within selectors against
    // document.activeElement, so the class toggle is the only path
    // we can assert deterministically. SearchBar's onfocus/onblur
    // handlers maintain the class — the test confirms that wiring.
    expect(wrapper.classList.contains("focused")).toBe(false);
    await fireEvent.focus(input);
    expect(wrapper.classList.contains("focused")).toBe(true);
    await fireEvent.blur(input);
    expect(wrapper.classList.contains("focused")).toBe(false);
  });

  it("Enter calls onsubmit with the trimmed query and skips empty/whitespace", async () => {
    const onsubmit = vi.fn<(q: string) => void>();
    const { getByTestId } = render(SearchBar, { props: { onsubmit } });
    const input = getByTestId("search-input") as HTMLInputElement;

    // Empty input → Enter must NOT submit.
    await fireEvent.keyDown(input, { key: "Enter" });
    expect(onsubmit).not.toHaveBeenCalled();

    // Whitespace-only → Enter still must NOT submit.
    await fireEvent.input(input, { target: { value: "   " } });
    await fireEvent.keyDown(input, { key: "Enter" });
    expect(onsubmit).not.toHaveBeenCalled();

    // Real query with surrounding whitespace → submit with trimmed value.
    await fireEvent.input(input, { target: { value: "  trees  " } });
    await fireEvent.keyDown(input, { key: "Enter" });
    expect(onsubmit).toHaveBeenCalledTimes(1);
    expect(onsubmit).toHaveBeenCalledWith("trees");
  });
});
