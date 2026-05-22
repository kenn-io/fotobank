import { computeJustified, type LayoutOptions } from "./justifiedLayout";
import type { ThumbStatus } from "../media/mediaStore.svelte";

// MediaLite is the projection VirtualGrid passes to MonthChunk. It
// carries only what the cell render needs: id (for click handlers),
// aspect (for justified layout), thumbUrl (for the <img> src), and
// thumbStatus (so MediaCell can branch between "render the image",
// "show shimmer", and "show failed/no-preview placeholder" instead
// of always attempting to load and falling back on 404).
export type MediaLite = {
  id: string;
  aspect: number;
  thumbUrl?: string;
  thumbStatus?: ThumbStatus;
};

// Reserved height for a rendered .day-header inside MonthChunk. Must
// stay in sync with the component's .day-header rule (16px top + 8px
// bottom padding + the taller of the label line-height and the
// GroupSelectButton in the trailing slot). The button (1px border +
// 2px padding + 11px font line-box) renders ~18px tall, pushing the
// header to ~42px in Chromium. Rounded up to a multiple of 4 so the
// reservation has a small buffer for cross-browser font metrics.
// Virtualizers and scrollbar scrubbers query intrinsicHeight to
// position month chunks; if the header isn't accounted for here the
// chunks below a labeled month will overlap or under-reserve their
// slot.
export const DAY_HEADER_HEIGHT = 44;

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
