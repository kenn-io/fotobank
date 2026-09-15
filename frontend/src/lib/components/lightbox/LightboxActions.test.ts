import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import LightboxActions from "./LightboxActions.svelte";
import { lightboxSession } from "../../lightbox/lightboxSession.svelte";
import { AppConfigStore } from "../../app/appConfig.svelte";
import type { Client } from "../../api/client";

function defaultAppConfig(): AppConfigStore {
  const c = {
    GET: async () => ({ data: undefined, error: { status: 0 } }),

listAlbums(params?: any, options?: any) { return (this as any).GET("/api/v1/albums", { params: { query: params }, ...options }); },
hiddenState(options?: any) { return (this as any).GET("/api/v1/auth/hidden/state", { ...options }); },
me(options?: any) { return (this as any).GET("/api/v1/me", { ...options }); },
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); }
} as unknown as Client;
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

describe("LightboxActions — Unhide keyed on media.hidden_at", () => {
  it("renders Unhide (not Share/Hide) when source is map AND media is hidden", () => {
    const { props } = fakeStores();
    lightboxSession.open({
      source: { kind: "map" },
      navIds: ["m1"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: null,
      returnHref: "/map?z=10&c=0,0&include_hidden=true",
      includeHidden: true,
    });
    const hiddenMedia = { ...fakeMedia, hidden_at: "2026-05-02T00:00:00Z" };
    const { queryByRole, getByRole } = render(LightboxActions, {
      props: {
        source: { kind: "map" },
        media: hiddenMedia,
        rawMedia,
        ...props,
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onDone: vi.fn(),
      } as never,
    });
    // Unhide present:
    expect(getByRole("button", { name: /unhide/i })).toBeTruthy();
    // Hide and Share suppressed (Hide because in unhide context;
    // Share because (a) unhide context AND (b) sharing-disabled by default):
    expect(queryByRole("button", { name: /^hide$/i })).toBeNull();
    expect(queryByRole("button", { name: /share/i })).toBeNull();
  });

  it("renders Hide (not Unhide) when source is map AND media is NOT hidden", () => {
    const { props } = fakeStores();
    lightboxSession.open({
      source: { kind: "map" },
      navIds: ["m1"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: null,
      returnHref: "/map?z=10&c=0,0",
    });
    // fakeMedia has no hidden_at — explicitly set null for clarity.
    const visibleMedia = { ...fakeMedia, hidden_at: null };
    const { queryByRole, getByRole } = render(LightboxActions, {
      props: {
        source: { kind: "map" },
        media: visibleMedia,
        rawMedia,
        ...props,
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onDone: vi.fn(),
      } as never,
    });
    expect(getByRole("button", { name: /^hide$/i })).toBeTruthy();
    expect(queryByRole("button", { name: /unhide/i })).toBeNull();
  });
});
