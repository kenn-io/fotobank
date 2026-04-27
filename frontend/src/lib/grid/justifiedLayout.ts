export type LayoutItem = { aspect: number };

export type LayoutOptions = {
  containerWidth: number;
  targetRowHeight: number;
  gap?: number;
  maxRowHeight?: number;
};

export type Row = {
  y: number;
  height: number;
  items: { index: number; x: number; width: number }[];
};

export type Layout = { rows: Row[]; totalHeight: number };

export function computeJustified(items: LayoutItem[], opts: LayoutOptions): Layout {
  const gap = opts.gap ?? 4;
  const maxH = opts.maxRowHeight ?? Math.ceil(opts.targetRowHeight * 1.6);
  const rows: Row[] = [];
  let y = 0;

  let pending: { index: number; aspect: number }[] = [];
  let pendingAspectSum = 0;

  const flush = (forceRow: boolean) => {
    if (pending.length === 0) return;
    // Row width budget = containerWidth - total gaps.
    const budget = opts.containerWidth - gap * (pending.length - 1);
    let height = budget / pendingAspectSum;
    if (forceRow) {
      // Final, possibly under-filled row: cap at targetRowHeight.
      height = Math.min(height, opts.targetRowHeight);
    } else {
      // At natural height (budget / pendingAspectSum) the row's total
      // width equals containerWidth, so any clamp UP to minH would push
      // total width past the container. Only clamp DOWN to maxH; for
      // very wide single items (e.g., a panorama) accept the short row
      // rather than overflow. Matches Flickr/Google-Photos behavior.
      height = Math.min(maxH, height);
    }
    let x = 0;
    const rowItems: Row["items"] = [];
    for (const p of pending) {
      const w = p.aspect * height;
      rowItems.push({ index: p.index, x, width: w });
      x += w + gap;
    }
    rows.push({ y, height, items: rowItems });
    y += height + gap;
    pending = [];
    pendingAspectSum = 0;
  };

  for (let i = 0; i < items.length; i++) {
    const aspect = Math.max(items[i]!.aspect, 0.05);
    pending.push({ index: i, aspect });
    pendingAspectSum += aspect;
    const widthIfPacked = pendingAspectSum * opts.targetRowHeight + gap * (pending.length - 1);
    if (widthIfPacked >= opts.containerWidth) {
      flush(false);
    }
  }
  flush(true);

  return { rows, totalHeight: rows.length === 0 ? 0 : y - gap };
}
