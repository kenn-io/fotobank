import { computeJustified, type LayoutOptions } from "./justifiedLayout";

export type MediaLite = { id: string; aspect: number; thumbUrl?: string };

// Reserved height for a rendered .day-header inside MonthChunk. Must
// stay in sync with the component's .day-header rule (16px top + 8px
// bottom padding + ~16px line height for the 12px font, rounded up).
// Virtualizers and scrollbar scrubbers query intrinsicHeight to
// position month chunks; if the header isn't accounted for here the
// chunks below a labeled month will overlap or under-reserve their
// slot.
export const DAY_HEADER_HEIGHT = 40;

export function computeMonthLayout(
  items: MediaLite[],
  opts: LayoutOptions,
  hasLabel = false,
) {
  const layout = computeJustified(items.map((m) => ({ aspect: m.aspect })), opts);
  // Empty months stay zero-height even when a label was requested:
  // there is nothing to render, so the chunk reserves no space.
  const headerHeight = hasLabel && items.length > 0 ? DAY_HEADER_HEIGHT : 0;
  return { layout, intrinsicHeight: layout.totalHeight + headerHeight };
}
