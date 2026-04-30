// frontend/src/lib/lightbox/modalStack.svelte.ts
//
// Central modal stack with top-down Esc dispatch. Each modal-ish
// component (Lightbox, AddToAlbumModal, ShareModal, ConfirmModal,
// RenameAlbumModal, BottomSheet-as-modal) registers itself on mount
// and pops on unmount. Esc events fire the topmost entry's handler;
// the handler is responsible for triggering its own close path
// (which leads to unmount → pop). dispatchEscape() does NOT auto-pop.

export type FocusTrapHandle = {
  pause(): void;
  resume(): void;
  release(): void;
};

export type ModalEntry = {
  id: string;
  onEscape: () => void;
  trap?: FocusTrapHandle;
};

export class ModalStack {
  private entries: ModalEntry[] = $state([]);

  push(entry: ModalEntry): void {
    const prev = this.top();
    this.entries = [...this.entries, entry];
    prev?.trap?.pause();
  }

  pop(id: string): void {
    const idx = this.entries.findIndex((e) => e.id === id);
    if (idx < 0) return;
    this.entries = [...this.entries.slice(0, idx), ...this.entries.slice(idx + 1)];
    this.top()?.trap?.resume();
  }

  isTopmost(id: string): boolean {
    return this.top()?.id === id;
  }

  top(): ModalEntry | null {
    return this.entries.length > 0 ? this.entries[this.entries.length - 1]! : null;
  }

  dispatchEscape(): boolean {
    const t = this.top();
    if (t === null) return false;
    t.onEscape();
    return true;
  }
}

// Module-level singleton — the stack is process-global; modals
// across the app register against the same instance. Mirrors the
// SelectionStore export pattern.
export const modalStack = new ModalStack();
