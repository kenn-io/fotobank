import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import MediaCell from "./MediaCell.svelte";

const mediaWithThumb = {
  id: "abc",
  aspect: 1.5,
  thumbUrl: "/api/v1/media/abc/thumb?size=grid&v=3",
  thumbStatus: "ready" as const,
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

  it("swaps to a shimmer when the img fires onerror on a ready thumb", async () => {
    // A 404 on a row whose status is "ready" means the file vanished
    // between the API list and the <img> request — typically a
    // thumb_version bump from a concurrent regenerate. The cell
    // shimmers to read as in-flight; the next refetch will land the
    // bumped ?v= URL and the imgError reset effect will re-attempt.
    const { container } = render(MediaCell, {
      media: mediaWithThumb, selected: false, onCellClick: () => {},
    });
    const img = container.querySelector("img")!;
    await fireEvent.error(img);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".shimmer")).not.toBeNull();
  });

  it("renders a placeholder when thumb has no preview", () => {
    // thumbStatus="no_preview" is the terminal "this format has no
    // grid thumb" state — render the static placeholder, not the
    // shimmer. tsconfig has exactOptionalPropertyTypes:true, so
    // MediaLite's optional `thumbUrl?: string` rejects an explicit
    // `undefined`; omit the property instead.
    const { container } = render(MediaCell, {
      media: { id: "x", aspect: 1, thumbStatus: "no_preview" as const },
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
      media: { id: "x", aspect: 1, thumbUrl: "/api/v1/media/x/thumb?size=grid&v=1", thumbStatus: "ready" as const },
      selected: false, onCellClick: () => {},
    });
    await fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".shimmer")).not.toBeNull();

    await rerender({
      media: { id: "x", aspect: 1, thumbUrl: "/api/v1/media/x/thumb?size=grid&v=2", thumbStatus: "ready" as const },
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
      media: { id: "x", aspect: 1, thumbUrl: url, thumbStatus: "ready" as const },
      selected: false, onCellClick: () => {},
    });
    await fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".shimmer")).not.toBeNull();

    // Rerender with a fresh object literal — same URL string. This
    // mirrors a parent rerender that emits a new MediaLite without
    // changing thumb_version.
    await rerender({
      media: { id: "x", aspect: 1, thumbUrl: url, thumbStatus: "ready" as const },
      selected: false, onCellClick: () => {},
    });
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".shimmer")).not.toBeNull();
  });
});
