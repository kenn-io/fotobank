import { describe, it, expect } from "vitest";
import { DAY_HEADER_HEIGHT, computeMonthLayout, type MediaLite } from "./MonthChunk.svelte";

describe("computeMonthLayout", () => {
  it("returns intrinsic height for content-visibility:auto skipping", () => {
    const items: MediaLite[] = Array.from({ length: 8 }, (_, i) => ({
      id: String(i), aspect: 1.5,
    }));
    const out = computeMonthLayout(items, { containerWidth: 900, targetRowHeight: 200, gap: 4 });
    expect(out.intrinsicHeight).toBeGreaterThan(0);
    expect(out.layout.rows.length).toBeGreaterThan(0);
  });

  it("treats empty months as zero-height", () => {
    const out = computeMonthLayout([], { containerWidth: 900, targetRowHeight: 200, gap: 4 });
    expect(out.intrinsicHeight).toBe(0);
  });

  it("reserves header height when label will be rendered", () => {
    // Without label flag: intrinsicHeight is just the cells area.
    // With label flag (Library route always passes month.key): the
    // chunk reserves DAY_HEADER_HEIGHT extra px so virtualizers don't
    // undercount labeled chunks. Required by Tasks 21/22 (sticky
    // month bar, year scrubber) which compute scroll offsets.
    const items: MediaLite[] = [{ id: "1", aspect: 1.5 }];
    const opts = { containerWidth: 900, targetRowHeight: 200, gap: 4 };
    const noLabel = computeMonthLayout(items, opts, false);
    const withLabel = computeMonthLayout(items, opts, true);
    expect(withLabel.intrinsicHeight).toBe(noLabel.intrinsicHeight + DAY_HEADER_HEIGHT);
    // layout.totalHeight is the inner cells area only — unchanged.
    expect(withLabel.layout.totalHeight).toBe(noLabel.layout.totalHeight);
  });

  it("keeps empty labeled chunks at zero height", () => {
    // hasLabel + items.length === 0 → still zero, because there is
    // nothing to render.
    const out = computeMonthLayout([], { containerWidth: 900, targetRowHeight: 200, gap: 4 }, true);
    expect(out.intrinsicHeight).toBe(0);
  });
});
