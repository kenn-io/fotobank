import { describe, it, expect } from "vitest";
import { computeMonthLayout, type MediaLite } from "./MonthChunk.svelte";

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
});
