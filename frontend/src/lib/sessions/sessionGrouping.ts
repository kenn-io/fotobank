import type { Media } from "../media/mediaStore.svelte";

export type Session = { id: string; items: Media[] };

export function groupIntoSessions(
  items: Media[],
  opts: { gapHours: number },
): Session[] {
  if (items.length === 0) return [];
  const sorted = [...items].sort((a, b) => +a.taken - +b.taken);
  const gapMs = opts.gapHours * 3600 * 1000;
  const out: Session[] = [];
  let current: Media[] = [];
  let prev: Media | null = null;
  for (const m of sorted) {
    if (prev && +m.taken - +prev.taken > gapMs) {
      out.push({ id: current[0]!.id, items: current });
      current = [];
    }
    current.push(m);
    prev = m;
  }
  if (current.length) out.push({ id: current[0]!.id, items: current });
  return out;
}
