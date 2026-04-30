// frontend/src/lib/lightbox/modalStack.svelte.ts
//
// Central modal stack with top-down Esc dispatch. Each modal-ish
// component (Lightbox, AddToAlbumModal, ShareModal, ConfirmModal,
// RenameAlbumModal, BottomSheet-as-modal) registers itself on mount
// and pops on unmount. Esc events fire the topmost entry's handler;
// the handler is responsible for triggering its own close path
// (which leads to unmount → pop). dispatchEscape() does NOT auto-pop,
// but it marks the top entry as "closing" so a second rapid Esc
// before unmount doesn't run the handler twice.
//
// onEscape may return `false` to reject the Esc (e.g. a modal with a
// pending in-flight save). A rejected dispatch leaves `closing`
// unset, so a later Esc after the operation finishes can still close
// the modal. Returning `true` or `void` is treated as accepted.

export type FocusTrapHandle = {
  pause(): void;
  resume(): void;
  release(): void;
};

export type ModalEntry = {
  id: string;
  onEscape: () => boolean | void;
  trap?: FocusTrapHandle;
};

type InternalEntry = ModalEntry & { closing: boolean };

export class ModalStack {
  private entries: InternalEntry[] = $state([]);

  push(entry: ModalEntry): void {
    const prev = this.topInternal();
    this.entries = [...this.entries, { ...entry, closing: false }];
    prev?.trap?.pause();
  }

  pop(id: string): void {
    const idx = this.entries.findIndex((e) => e.id === id);
    if (idx < 0) return;
    const wasTopmost = idx === this.entries.length - 1;
    this.entries = [...this.entries.slice(0, idx), ...this.entries.slice(idx + 1)];
    // Only resume the new top's trap if we removed the topmost entry.
    // Out-of-order unmounts (a non-top modal leaves first) must NOT
    // resume the current top — its trap is already active.
    if (wasTopmost) this.topInternal()?.trap?.resume();
  }

  isTopmost(id: string): boolean {
    return this.topInternal()?.id === id;
  }

  top(): ModalEntry | null {
    const t = this.topInternal();
    if (t === null) return null;
    const { closing: _closing, ...rest } = t;
    return rest;
  }

  /**
   * Fire the topmost entry's onEscape handler exactly once until the
   * entry unmounts (and pops) — unless onEscape returns `false`, in
   * which case the dispatch is treated as rejected and a later Esc
   * can still fire the handler again. Subsequent dispatchEscape
   * calls while the same top entry is still on the stack and was
   * accepted return true (handled) without re-invoking the handler.
   * Returns false only when the stack is empty.
   */
  dispatchEscape(): boolean {
    const t = this.topInternal();
    if (t === null) return false;
    if (t.closing) return true;
    const result = t.onEscape();
    // Treat void/undefined/true as accepted. Only an explicit `false`
    // marks the dispatch as rejected so the entry stays escapable.
    if (result !== false) t.closing = true;
    return true;
  }

  private topInternal(): InternalEntry | null {
    return this.entries.length > 0 ? this.entries[this.entries.length - 1]! : null;
  }
}

// Module-level singleton — the stack is process-global; modals
// across the app register against the same instance. Mirrors the
// SelectionStore export pattern.
export const modalStack = new ModalStack();
