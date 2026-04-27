import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import MediaCell from "./MediaCell.svelte";

const mediaWithThumb = {
  id: "abc",
  aspect: 1.5,
  thumbUrl: "/api/v1/media/abc/thumb?size=grid&v=3",
};

describe("MediaCell", () => {
  it("renders an anchor to /media/:id with aria-label and the thumb img", () => {
    const { container } = render(MediaCell, {
      media: mediaWithThumb,
      selected: false,
      onCellClick: () => {},
    });
    const a = container.querySelector("a")!;
    expect(a.getAttribute("href")).toBe("/media/abc");
    expect(a.getAttribute("aria-label")).toBe("Photo abc");
    expect(a.classList.contains("selected")).toBe(false);
    const img = container.querySelector("img")!;
    expect(img.getAttribute("src")).toBe(mediaWithThumb.thumbUrl);
  });

  it("applies the selected class when selected=true", () => {
    const { container } = render(MediaCell, {
      media: mediaWithThumb,
      selected: true,
      onCellClick: () => {},
    });
    expect(container.querySelector("a")?.classList.contains("selected")).toBe(true);
  });

  it("invokes onCellClick on click", async () => {
    const onCellClick = vi.fn();
    const { container } = render(MediaCell, {
      media: mediaWithThumb, selected: false, onCellClick,
    });
    await fireEvent.click(container.querySelector("a")!);
    expect(onCellClick).toHaveBeenCalledTimes(1);
  });

  it("swaps to a placeholder div when the img fires onerror", async () => {
    const { container } = render(MediaCell, {
      media: mediaWithThumb, selected: false, onCellClick: () => {},
    });
    const img = container.querySelector("img")!;
    await fireEvent.error(img);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".placeholder")).not.toBeNull();
  });

  it("renders a placeholder when thumbUrl is missing", () => {
    // tsconfig has exactOptionalPropertyTypes:true, so MediaLite's
    // optional `thumbUrl?: string` rejects an explicit `undefined`.
    // Omit the property to exercise the same placeholder branch.
    const { container } = render(MediaCell, {
      media: { id: "x", aspect: 1 },
      selected: false, onCellClick: () => {},
    });
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".placeholder")).not.toBeNull();
  });

  it("re-attempts the image when media.thumbUrl changes after an error", async () => {
    // Regression-locks the thumb-regenerate cache-bust path: when an
    // operator runs `thumbs regenerate`, thumb_version bumps and a
    // cached row still pointing at ?v=N 404s. Once a refetch lands the
    // ?v=N+1 URL, MediaCell must re-attempt the load instead of
    // staying on the placeholder forever.
    const { container, rerender } = render(MediaCell, {
      media: { id: "x", aspect: 1, thumbUrl: "/api/v1/media/x/thumb?size=grid&v=1" },
      selected: false, onCellClick: () => {},
    });
    await fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".placeholder")).not.toBeNull();

    await rerender({
      media: { id: "x", aspect: 1, thumbUrl: "/api/v1/media/x/thumb?size=grid&v=2" },
      selected: false, onCellClick: () => {},
    });
    const img = container.querySelector("img");
    expect(img).not.toBeNull();
    expect(img!.getAttribute("src")).toBe("/api/v1/media/x/thumb?size=grid&v=2");
  });

  it("stays on the placeholder when the parent rerenders with a fresh media object but the same thumbUrl", async () => {
    // VirtualGrid's toLite() allocates a new MediaLite per render even
    // when the underlying fields are unchanged, so the cell receives
    // a fresh prop reference on every parent rerender. The reset
    // effect must key on the URL string, not the prop reference, or
    // every unrelated rerender clears imgError and re-fetches the
    // same broken URL — defeating the placeholder fallback.
    const url = "/api/v1/media/x/thumb?size=grid&v=1";
    const { container, rerender } = render(MediaCell, {
      media: { id: "x", aspect: 1, thumbUrl: url },
      selected: false, onCellClick: () => {},
    });
    await fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".placeholder")).not.toBeNull();

    // Rerender with a fresh object literal — same URL string. This
    // mirrors a parent rerender that emits a new MediaLite without
    // changing thumb_version.
    await rerender({
      media: { id: "x", aspect: 1, thumbUrl: url },
      selected: false, onCellClick: () => {},
    });
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".placeholder")).not.toBeNull();
  });
});
