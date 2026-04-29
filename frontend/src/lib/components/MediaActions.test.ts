import { render, fireEvent } from "@testing-library/svelte";
import { describe, it, expect, vi } from "vitest";
import MediaActions from "./MediaActions.svelte";

describe("MediaActions", () => {
  it("renders Add to album and Share buttons by default", () => {
    const { getByRole } = render(MediaActions, {
      props: { mediaIds: ["m1"], onAdd: vi.fn(), onShare: vi.fn() },
    });
    expect(getByRole("button", { name: "Add to album" })).not.toBeNull();
    expect(getByRole("button", { name: "Share" })).not.toBeNull();
  });

  it("renders Remove from this album when context=album and onRemove is set", () => {
    const { getByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "album",
        albumId: "a1",
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onRemove: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Remove from this album" })).not.toBeNull();
  });

  it("does NOT render Remove when context!=album", () => {
    const { queryByRole } = render(MediaActions, {
      props: { mediaIds: ["m1"], onAdd: vi.fn(), onShare: vi.fn() },
    });
    expect(queryByRole("button", { name: "Remove from this album" })).toBeNull();
  });

  it("clicking Add invokes onAdd with mediaIds", async () => {
    const onAdd = vi.fn();
    const { getByRole } = render(MediaActions, {
      props: { mediaIds: ["m1", "m2"], onAdd, onShare: vi.fn() },
    });
    await fireEvent.click(getByRole("button", { name: "Add to album" }));
    expect(onAdd).toHaveBeenCalledWith(["m1", "m2"]);
  });

  it("clicking Share invokes onShare with mediaIds", async () => {
    const onShare = vi.fn();
    const { getByRole } = render(MediaActions, {
      props: { mediaIds: ["m1"], onAdd: vi.fn(), onShare },
    });
    await fireEvent.click(getByRole("button", { name: "Share" }));
    expect(onShare).toHaveBeenCalledWith(["m1"]);
  });

  it("hides all buttons when mediaIds is empty (defense)", () => {
    const { queryByRole } = render(MediaActions, {
      props: { mediaIds: [], onAdd: vi.fn(), onShare: vi.fn() },
    });
    expect(queryByRole("button", { name: "Add to album" })).toBeNull();
    expect(queryByRole("button", { name: "Share" })).toBeNull();
  });
});
