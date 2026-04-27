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
}

export const selection = new SelectionStore();
