import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import LightboxActions from "./LightboxActions.svelte";
import { lightboxSession } from "../../lightbox/lightboxSession.svelte";
import { AppConfigStore } from "../../app/appConfig.svelte";
import type { Client } from "../../api/client";

function defaultAppConfig(): AppConfigStore {
  const c = {
    GET: async () => ({ data: undefined, error: { status: 0 } }),
  } as unknown as Pick<Client, "GET">;
  return new AppConfigStore(c);
}

const fakeMedia = {
  id: "m1",
  timestamp: "",
  aspect: 1,
  thumbUrl: "",
  taken: new Date(),
  thumbVersion: 0,
};
const rawMedia = { id: "m1", thumb_version: 0, width: 1, height: 1 };

function fakeStores() {
  const removeMany = vi.fn();
  const mergeRaw = vi.fn();
  const markStale = vi.fn();
  const hide = vi.fn().mockResolvedValue({ succeeded: ["m1"], failed: [] });
  const unhide = vi.fn().mockResolvedValue({ succeeded: ["m1"], failed: [] });
  const push = vi.fn();
  return {
    mocks: { removeMany, mergeRaw, markStale, hide, unhide, push },
    props: {
      mediaStore: { removeMany, mergeRaw } as never,
      albumsStore: { markStale } as never,
      hiddenStore: { configured: true, hide, unhide } as never,
      toastStore: { push } as never,
    },
  };
}

describe("LightboxActions", () => {
  it("library hide → mediaStore.removeMany + onDone", async () => {
    const { mocks, props } = fakeStores();
    lightboxSession.open({
      source: { kind: "library" },
      navIds: ["m1", "m2"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: null,
      returnHref: "/library",
    });
    const onDone = vi.fn();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const { getByRole } = render(LightboxActions, {
      props: {
        source: { kind: "library" },
        media: fakeMedia,
        rawMedia,
        ...props,
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onDone,
      } as never,
    });
    await fireEvent.click(getByRole("button", { name: /hide/i }));
    await Promise.resolve();
    await Promise.resolve();
    expect(mocks.removeMany).toHaveBeenCalledWith(["m1"]);
    expect(onDone).toHaveBeenCalledWith("hide", ["m1"]);
  });
});
