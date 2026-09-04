import { describe, it, expect, vi, afterEach } from "vitest";
import { fireEvent, render } from "@testing-library/svelte";
import SearchBar from "./SearchBar.svelte";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("SearchBar", () => {
  it("renders an input with the documented placeholder", () => {
    const { getByRole } = render(SearchBar, {
      props: { onsubmit: () => {} },
    });
    const input = getByRole("searchbox", { name: "Search" }) as HTMLInputElement;
    expect(input).toBeTruthy();
    expect(input.placeholder).toBe("Search photos, cameras, places, dates…");
  });

  it("⌘K (Meta+K) and Ctrl+K both focus the input", () => {
    // Render the component twice so each shortcut is verified against
    // a fresh component (and a fresh effect-bound listener). Anything
    // less and the second dispatch could be no-op'd by the first key
    // already having focused the input.
    const meta = render(SearchBar, { props: { onsubmit: () => {} } });
    const metaInput = meta.getByRole("searchbox", { name: "Search" }) as HTMLInputElement;
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
    const ctrlInput = ctrl.getByRole("searchbox", { name: "Search" }) as HTMLInputElement;
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

  it("configures the shared shortcut hint", () => {
    const { container } = render(SearchBar, {
      props: { onsubmit: () => {} },
    });
    expect(container.querySelector("kbd")?.getAttribute("aria-label")).toBe("⌘ K");
  });

  it("Enter calls onsubmit with the trimmed query and skips empty/whitespace", async () => {
    const onsubmit = vi.fn<(q: string) => void>();
    const { getByRole } = render(SearchBar, { props: { onsubmit } });
    const input = getByRole("searchbox", { name: "Search" }) as HTMLInputElement;

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

  it("clears a query and returns focus to the search field", async () => {
    const { getByRole } = render(SearchBar, {
      props: { onsubmit: () => {} },
    });
    const input = getByRole("searchbox", { name: "Search" }) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "trees" } });

    await fireEvent.click(getByRole("button", { name: "Clear search" }));

    expect(input.value).toBe("");
    expect(document.activeElement).toBe(input);
  });

  it("updates the field when the host query changes", async () => {
    const view = render(SearchBar, {
      props: { query: "trees", onsubmit: () => {} },
    });
    const input = view.getByRole("searchbox", { name: "Search" }) as HTMLInputElement;
    expect(input.value).toBe("trees");

    await view.rerender({ query: "portraits", onsubmit: () => {} });

    expect(input.value).toBe("portraits");
  });
});
