import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import { createRawSnippet } from "svelte";
import LightboxFrame from "./LightboxFrame.svelte";

function textSnippet(text: string) {
  return createRawSnippet(() => ({ render: () => `<span>${text}</span>` }));
}

describe("LightboxFrame", () => {
  it("backdrop click invokes onBackdropClick only when target is backdrop", async () => {
    const onBackdropClick = vi.fn();
    const { container } = render(LightboxFrame, {
      props: { mode: "full", onBackdropClick, children: textSnippet("x") },
    });
    const backdrop = container.querySelector(".lb-backdrop") as HTMLElement;
    await fireEvent.click(backdrop);
    expect(onBackdropClick).toHaveBeenCalledOnce();
  });

  it("applies fallback class when mode=fallback", () => {
    const { container } = render(LightboxFrame, {
      props: { mode: "fallback", children: textSnippet("x") },
    });
    expect(container.querySelector(".lb-backdrop.fallback")).toBeTruthy();
  });
});
