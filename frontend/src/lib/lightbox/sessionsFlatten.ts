// frontend/src/lib/lightbox/sessionsFlatten.ts
//
// Source-list flattening for lightbox navIds. Library and Sessions
// share the same mediaStore.months ordering (newest first within month,
// newest month first); the flatten result is identical. The two
// exported names exist so callers in source routes are explicit
// about which list they're capturing.

import type { Month } from "../media/mediaStore.svelte";

export function flattenLibraryIds(months: Month[]): string[] {
  const out: string[] = [];
  for (const m of months) {
    for (const it of m.items) out.push(it.id);
  }
  return out;
}

export const flattenSessionIds = flattenLibraryIds;
