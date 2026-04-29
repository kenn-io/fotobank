// frontend/src/lib/selection/selectionStore.svelte.ts
export class SelectionStore {
  ids = $state<Set<string>>(new Set());
  lastAnchor: string | null = $state(null);

  toggle(id: string) {
    const next = new Set(this.ids);
    if (next.has(id)) next.delete(id); else next.add(id);
    this.ids = next;
    this.lastAnchor = id;
  }

  set(id: string, on: boolean) {
    const next = new Set(this.ids);
    if (on) next.add(id); else next.delete(id);
    this.ids = next;
    this.lastAnchor = id;
  }

  range(target: string, ordered: string[]) {
    const anchor = this.lastAnchor ?? target;
    const i = ordered.indexOf(anchor);
    const j = ordered.indexOf(target);
    if (i < 0 || j < 0) { this.toggle(target); return; }
    const [lo, hi] = i < j ? [i, j] : [j, i];
    const next = new Set(this.ids);
    for (let k = lo; k <= hi; k++) {
      const id = ordered[k];
      if (id) next.add(id);
    }
    this.ids = next;
    this.lastAnchor = target;
  }

  clear() {
    this.ids = new Set();
    this.lastAnchor = null;
  }

  addAll(ids: Iterable<string>): void {
    let last: string | undefined;
    for (const id of ids) {
      this.ids.add(id);
      last = id;
    }
    if (last !== undefined) {
      this.lastAnchor = last;
    }
  }

  removeAll(ids: Iterable<string>): void {
    for (const id of ids) {
      this.ids.delete(id);
    }
    // lastAnchor intentionally untouched — deselecting a group should
    // not move the range-select anchor (§13.4).
  }

  hasAll(ids: Iterable<string>): boolean {
    let any = false;
    for (const id of ids) {
      any = true;
      if (!this.ids.has(id)) return false;
    }
    return any; // empty iterable returns false
  }
}

export const selection = new SelectionStore();
