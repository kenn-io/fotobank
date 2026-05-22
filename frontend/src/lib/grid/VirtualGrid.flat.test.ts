import { render, fireEvent } from "@testing-library/svelte";
import { describe, it, expect, beforeAll, vi } from "vitest";
import VirtualGrid from "./VirtualGrid.svelte";
import VirtualGridHeaderActionFixture from "./VirtualGridHeaderActionFixture.svelte";
import type { Month } from "../media/mediaStore.svelte";
import { router } from "../router/router.svelte";

// VirtualGrid wires ResizeObserver + IntersectionObserver in $effect
// blocks. jsdom doesn't ship either, so we stub no-op implementations
// — the chrome-presence assertions don't need observation callbacks
// to fire.
beforeAll(() => {
  class NoopResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  class NoopIntersectionObserver {
    root = null;
    rootMargin = "";
    thresholds: number[] = [];
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords(): IntersectionObserverEntry[] {
      return [];
    }
  }
  vi.stubGlobal("ResizeObserver", NoopResizeObserver);
  vi.stubGlobal("IntersectionObserver", NoopIntersectionObserver);
});

// Test fixtures use the lighter MediaLite shape ({id, aspect, thumbUrl})
// rather than full Media rows. The chrome-presence assertions below
// don't exercise the cell-click range path (which reads from
// orderedIds), so the items only ever flow through toLite() — which
// reads exactly those three fields. Cast via `as unknown as Month[]`
// keeps the test minimal without rebuilding full Media fixtures.
const months = [
  {
    key: "album:abc",
    items: [
      { id: "m1", aspect: 1.5, thumbUrl: "/t/m1" },
      { id: "m2", aspect: 1.0, thumbUrl: "/t/m2" },
    ],
  },
] as unknown as Month[];

describe("VirtualGrid timelineChrome=false", () => {
  it("does not render StickyMonthBar in flat mode", () => {
    const { container } = render(VirtualGrid, {
      props: { months, onLoadMore: () => {}, targetRowHeight: 200, timelineChrome: false },
    });
    // StickyMonthBar's root is `.bar` (see StickyMonthBar.svelte). In
    // flat mode it must not be wired in at all.
    expect(container.querySelector(".bar")).toBeNull();
  });

  it("does not render YearScrubber in flat mode", () => {
    const { container } = render(VirtualGrid, {
      props: { months, onLoadMore: () => {}, targetRowHeight: 200, timelineChrome: false },
    });
    // YearScrubber's root is `.scrubber` (see YearScrubber.svelte).
    expect(container.querySelector(".scrubber")).toBeNull();
  });

  it("does not render the MonthChunk header in flat mode", () => {
    const { container } = render(VirtualGrid, {
      props: { months, onLoadMore: () => {}, targetRowHeight: 200, timelineChrome: false },
    });
    expect(container.querySelector("header.day-header")).toBeNull();
  });

  it("still renders chrome when timelineChrome defaults to true", () => {
    const { container } = render(VirtualGrid, {
      props: { months, onLoadMore: () => {}, targetRowHeight: 200 },
    });
    // YearScrubber renders unconditionally given months. StickyMonthBar
    // only renders once IntersectionObserver fires (unreliable under
    // jsdom), so assert against the scrubber as the chrome witness.
    expect(container.querySelector(".scrubber")).not.toBeNull();
  });
});

describe("VirtualGrid headerAction forwarding", () => {
  it("renders the headerAction snippet on each MonthChunk with the correct month", () => {
    const twoMonths = [
      { key: "2024-04", items: [{ id: "m1", aspect: 1.5, thumbUrl: "/t/m1" }] },
      { key: "2024-03", items: [{ id: "m2", aspect: 1.0, thumbUrl: "/t/m2" }] },
    ] as unknown as Month[];
    const { container } = render(VirtualGridHeaderActionFixture, {
      props: { months: twoMonths },
    });
    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>("button.ha"));
    expect(buttons).toHaveLength(2);
    // Snippet `headerAction(month)` should receive the per-iteration `month`,
    // not capture the loop variable. Verify by reading data-month and text
    // on each rendered button — they must match the corresponding month.key.
    const pairs = buttons.map((b) => [b.dataset["month"], b.textContent?.trim()]);
    expect(pairs).toEqual([
      ["2024-04", "act:2024-04"],
      ["2024-03", "act:2024-03"],
    ]);
  });

  it("does not render headerAction when prop is omitted", () => {
    const { container } = render(VirtualGrid, {
      props: { months, onLoadMore: () => {}, targetRowHeight: 200 },
    });
    expect(container.querySelector("button.ha")).toBeNull();
  });
});

describe("VirtualGrid onOpenMedia", () => {
  it("invokes onOpenMedia(id) instead of router.navigate when prop is provided", async () => {
    const onOpenMedia = vi.fn();
    const navSpy = vi.spyOn(router, "navigate").mockImplementation(() => {});
    const { container } = render(VirtualGrid, {
      props: { months, onLoadMore: () => {}, targetRowHeight: 200, onOpenMedia },
    });
    const link = container.querySelector<HTMLAnchorElement>('a[data-media-id="m1"]');
    expect(link).not.toBeNull();
    await fireEvent.click(link!);
    expect(onOpenMedia).toHaveBeenCalledWith("m1");
    expect(navSpy).not.toHaveBeenCalled();
    navSpy.mockRestore();
  });

  it("falls back to router.navigate(/media/:id) when onOpenMedia is not provided", async () => {
    const navSpy = vi.spyOn(router, "navigate").mockImplementation(() => {});
    const { container } = render(VirtualGrid, {
      props: { months, onLoadMore: () => {}, targetRowHeight: 200 },
    });
    const link = container.querySelector<HTMLAnchorElement>('a[data-media-id="m1"]');
    expect(link).not.toBeNull();
    await fireEvent.click(link!);
    expect(navSpy).toHaveBeenCalledWith("/media/m1");
    navSpy.mockRestore();
  });
});
