import { render, fireEvent } from "@testing-library/svelte";
import { describe, it, expect, vi } from "vitest";
import ShareModal from "./ShareModal.svelte";

describe("ShareModal media_set", () => {
  it("primary button is disabled until grantee parses", () => {
    const onCreate = vi.fn();
    const { getByRole } = render(ShareModal, {
      props: {
        target: { type: "media_set" as const, mediaIds: ["m1"] },
        onCreate,
        onClose: vi.fn(),
      },
    });
    const primary = getByRole("button", { name: "Create share" });
    expect(primary.hasAttribute("disabled")).toBe(true);
  });

  it("enables primary once grantee is valid", async () => {
    const { getByRole, getByPlaceholderText } = render(ShareModal, {
      props: {
        target: { type: "media_set" as const, mediaIds: ["m1"] },
        onCreate: vi.fn(),
        onClose: vi.fn(),
      },
    });
    const input = getByPlaceholderText("myhub:bob");
    await fireEvent.input(input, { target: { value: "h:b" } });
    expect(getByRole("button", { name: "Create share" }).hasAttribute("disabled")).toBe(false);
  });

  it("disables primary with media_set > 1000 helper text", () => {
    const { getByRole, getByText } = render(ShareModal, {
      props: {
        target: { type: "media_set" as const, mediaIds: Array.from({ length: 1500 }, (_, i) => `m${i}`) },
        onCreate: vi.fn(),
        onClose: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Create share" }).hasAttribute("disabled")).toBe(true);
    expect(getByText(/Selection too large/)).not.toBeNull();
  });

  it("submit calls onCreate with parsed grantee object and proper body shape", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    const { getByRole, getByPlaceholderText, getByLabelText } = render(ShareModal, {
      props: {
        target: { type: "media_set" as const, mediaIds: ["m1", "m2"] },
        onCreate,
        onClose: vi.fn(),
      },
    });
    await fireEvent.input(getByPlaceholderText("myhub:bob"), { target: { value: "myhub:bob" } });
    await fireEvent.input(getByLabelText(/Label/), { target: { value: "Trip share" } });
    await fireEvent.click(getByLabelText(/Allow download/));
    await fireEvent.click(getByRole("button", { name: "Create share" }));
    expect(onCreate).toHaveBeenCalledWith({
      target_type: "media_set",
      media_ids: ["m1", "m2"],
      grantee: { hub: "myhub", user_id: "bob" },
      label: "Trip share",
      allow_download: true,
    });
  });
});

describe("ShareModal album_live", () => {
  it("title reflects album-share context and submit uses album_id", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    const { getByRole, getByPlaceholderText, getByText } = render(ShareModal, {
      props: {
        target: { type: "album_live" as const, albumId: "a1", albumName: "Italy 2025" },
        onCreate,
        onClose: vi.fn(),
      },
    });
    expect(getByText(/Share album: Italy 2025/)).not.toBeNull();
    await fireEvent.input(getByPlaceholderText("myhub:bob"), { target: { value: "h:b" } });
    await fireEvent.click(getByRole("button", { name: "Create share" }));
    expect(onCreate).toHaveBeenCalledWith({
      target_type: "album_live",
      album_id: "a1",
      grantee: { hub: "h", user_id: "b" },
      label: "",
      allow_download: false,
    });
  });
});
