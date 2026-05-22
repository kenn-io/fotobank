// frontend/src/lib/lightbox/lightboxNav.svelte.ts
//
// Pure derivation of nav state from a navIds list and the active id.
// Lightbox.svelte calls computeNav() inside a $derived so prev/next
// recompute when either input changes.

export type Nav = {
  index: number; // -1 if active id is not in navIds
  prevId: string | null;
  nextId: string | null;
  hasPrev: boolean;
  hasNext: boolean;
};

export function computeNav(navIds: string[], activeId: string): Nav {
  const index = navIds.indexOf(activeId);
  if (index < 0) {
    return { index: -1, prevId: null, nextId: null, hasPrev: false, hasNext: false };
  }
  const prevId = index > 0 ? navIds[index - 1] ?? null : null;
  const nextId = index < navIds.length - 1 ? navIds[index + 1] ?? null : null;
  return {
    index,
    prevId,
    nextId,
    hasPrev: prevId !== null,
    hasNext: nextId !== null,
  };
}
