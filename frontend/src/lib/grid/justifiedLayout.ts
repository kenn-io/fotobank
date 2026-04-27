export type LayoutItem = { aspect: number };

export type LayoutOptions = {
  containerWidth: number;
  targetRowHeight: number;
  gap?: number;
  minRowHeight?: number;
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
  const minH = opts.minRowHeight ?? Math.floor(opts.targetRowHeight * 0.6);
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
      height = Math.max(minH, Math.min(maxH, height));
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
