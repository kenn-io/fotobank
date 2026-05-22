// frontend/src/lib/lightbox/sessionsFlatten.ts
//
// Source-list flattening for lightbox navIds. Library walks
// mediaStore.months directly (newest month first, newest within each).
// Sessions reorders items into time-ascending clusters via
// groupIntoSessions, so its navIds must walk that grouping — otherwise
// prev/next in the lightbox doesn't match the visible Sessions grid.

import type { Month } from "../media/mediaStore.svelte";
import { groupIntoSessions } from "../sessions/sessionGrouping";

export function flattenLibraryIds(months: Month[]): string[] {
  const out: string[] = [];
  for (const m of months) {
    for (const it of m.items) out.push(it.id);
  }
  return out;
}

export function flattenSessionIds(months: Month[]): string[] {
  const flat = months.flatMap((m) => m.items);
  const sessions = groupIntoSessions(flat, { gapHours: 4 });
  const out: string[] = [];
  for (const s of sessions) {
    for (const it of s.items) out.push(it.id);
  }
  return out;
}
