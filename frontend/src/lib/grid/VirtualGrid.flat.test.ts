import { render } from "@testing-library/svelte";
import { describe, it, expect, beforeAll, vi } from "vitest";
import VirtualGrid from "./VirtualGrid.svelte";
import type { Month } from "../media/mediaStore.svelte";

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
