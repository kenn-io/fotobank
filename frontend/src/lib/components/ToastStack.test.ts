import { describe, it, expect } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import ToastStack from "./ToastStack.svelte";
import { ToastStore } from "../toasts/toastStore.svelte";

describe("ToastStack", () => {
  it("renders toast messages", () => {
    const store = new ToastStore();
    store.push({ message: "Upload complete" });
    store.push({ message: "Sync failed", kind: "error" });
    const { container } = render(ToastStack, { props: { toastStore: store } });
    expect(container.textContent).toContain("Upload complete");
    expect(container.textContent).toContain("Sync failed");
  });

  it("renders nothing when the list is empty", () => {
    const store = new ToastStore();
    const { container } = render(ToastStack, { props: { toastStore: store } });
    const stack = container.querySelector(".toast-stack");
    // Stack element may be absent or empty when no toasts exist.
    if (stack) {
      expect(stack.children).toHaveLength(0);
    } else {
      expect(container.textContent?.trim()).toBe("");
    }
  });

  it("dismiss button removes the toast", async () => {
    const store = new ToastStore();
    store.push({ message: "Remove me" });
    const { container } = render(ToastStack, { props: { toastStore: store } });
    const dismissBtn = container.querySelector("button[aria-label='Dismiss']") as HTMLButtonElement;
    expect(dismissBtn).not.toBeNull();
    await fireEvent.click(dismissBtn);
    expect(store.items).toHaveLength(0);
  });

  it("renders details when provided", () => {
    const store = new ToastStore();
    store.push({ message: "Partial failure", details: ["id1 failed", "id2 failed"] });
    const { container } = render(ToastStack, { props: { toastStore: store } });
    expect(container.textContent).toContain("id1 failed");
    expect(container.textContent).toContain("id2 failed");
  });
});
