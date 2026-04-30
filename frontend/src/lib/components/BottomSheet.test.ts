import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import { createRawSnippet } from "svelte";
import BottomSheet from "./BottomSheet.svelte";

function textSnippet(text: string) {
  return createRawSnippet(() => ({ render: () => `<span>${text}</span>` }));
}

describe("BottomSheet", () => {
  it("renders content slot", () => {
    const onClose = vi.fn();
    const { getByText } = render(BottomSheet, {
      props: { id: "bs1", onClose, snap: "peek", children: textSnippet("hello") },
    });
    expect(getByText("hello")).toBeTruthy();
  });

  it("backdrop click invokes onClose", async () => {
    const onClose = vi.fn();
    const { container } = render(BottomSheet, {
      props: { id: "bs1", onClose, snap: "peek", children: textSnippet("x") },
    });
    const backdrop = container.querySelector(".bs-backdrop") as HTMLElement;
    expect(backdrop).toBeTruthy();
    await fireEvent.click(backdrop);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("clicks inside the sheet do not close", async () => {
    const onClose = vi.fn();
    const { container } = render(BottomSheet, {
      props: { id: "bs1", onClose, snap: "peek", children: textSnippet("x") },
    });
    const sheet = container.querySelector(".bs-sheet") as HTMLElement;
    await fireEvent.click(sheet);
    expect(onClose).not.toHaveBeenCalled();
  });
});
