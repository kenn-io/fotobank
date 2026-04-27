import { describe, it, expect } from "vitest";
import { computeJustified } from "./justifiedLayout";

describe("computeJustified", () => {
  it("packs items at target row height when widths fit", () => {
    const items = [
      { aspect: 1.5 }, { aspect: 1.5 }, { aspect: 1.5 },
    ];
    const layout = computeJustified(items, { containerWidth: 900, targetRowHeight: 200, gap: 0 });
    expect(layout.rows).toHaveLength(1);
    expect(layout.rows[0]?.height).toBeCloseTo(200, 0);
    expect(layout.rows[0]?.items).toHaveLength(3);
  });

  it("breaks rows when width exceeds container", () => {
    const items = Array.from({ length: 10 }, () => ({ aspect: 1.5 }));
    const layout = computeJustified(items, { containerWidth: 900, targetRowHeight: 200, gap: 4 });
    expect(layout.rows.length).toBeGreaterThan(1);
    for (const row of layout.rows) {
      const totalW = row.items.reduce((s, it) => s + it.width, 0) + (row.items.length - 1) * 4;
      expect(totalW).toBeLessThanOrEqual(900 + 1);
    }
  });

  it("preserves aspect ratios within ±2px after rounding", () => {
    const items = [{ aspect: 1.5 }, { aspect: 0.66 }, { aspect: 1.0 }];
    const layout = computeJustified(items, { containerWidth: 800, targetRowHeight: 180, gap: 0 });
    for (const row of layout.rows) {
      for (const it of row.items) {
        const expectedAspect = items[it.index]!.aspect;
        const observedAspect = it.width / row.height;
        expect(Math.abs(observedAspect - expectedAspect)).toBeLessThan(0.05);
      }
    }
  });

  it("returns empty rows for empty input", () => {
    expect(computeJustified([], { containerWidth: 800, targetRowHeight: 200, gap: 0 }).rows).toEqual([]);
  });
});
