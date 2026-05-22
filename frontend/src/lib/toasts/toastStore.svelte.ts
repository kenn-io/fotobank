// frontend/src/lib/toasts/toastStore.svelte.ts

export type Toast = {
  id: string;
  message: string;
  details?: string[];
  kind?: "info" | "error";
};

export class ToastStore {
  items: Toast[] = $state([]);

  push(toast: Omit<Toast, "id">): string {
    const id = crypto.randomUUID();
    this.items = [...this.items, { ...toast, id }];
    return id;
  }

  dismiss(id: string): void {
    this.items = this.items.filter((t) => t.id !== id);
  }

  clear(): void {
    this.items = [];
  }
}
