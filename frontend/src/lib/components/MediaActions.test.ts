import { render, fireEvent } from "@testing-library/svelte";
import { describe, it, expect, vi } from "vitest";
import MediaActions from "./MediaActions.svelte";
import { AppConfigStore } from "../app/appConfig.svelte";
import type { Client } from "../api/client";

async function makeAppConfig(sharingEnabled: boolean): Promise<AppConfigStore> {
  const client = {
    GET: async () => ({
      data: {
        principal: { hub: "h", user_id: "u", handle: "" },
        scopes: [],
        features: { sharing_enabled: sharingEnabled },
      },
    }),
  } as unknown as Pick<Client, "GET">;
  const cfg = new AppConfigStore(client);
  await cfg.load();
  return cfg;
}

function defaultAppConfig(): AppConfigStore {
  // Defaults to sharingEnabled=false. Useful when a test wants the gate
  // closed without an extra await.
  const client = {
    GET: async () => ({ data: undefined, error: { status: 0 } }),
  } as unknown as Pick<Client, "GET">;
  return new AppConfigStore(client);
}

describe("MediaActions", () => {
  it("renders Add to album and Share buttons by default", async () => {
    const appConfig = await makeAppConfig(true);
    const { getByRole } = render(MediaActions, {
      props: { mediaIds: ["m1"], appConfig, onAdd: vi.fn(), onShare: vi.fn() },
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
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onRemove: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Remove from this album" })).not.toBeNull();
  });

  it("does NOT render Remove when context!=album", () => {
    const { queryByRole } = render(MediaActions, {
      props: { mediaIds: ["m1"], appConfig: defaultAppConfig(), onAdd: vi.fn(), onShare: vi.fn() },
    });
    expect(queryByRole("button", { name: "Remove from this album" })).toBeNull();
  });

  it("clicking Add invokes onAdd with mediaIds", async () => {
    const onAdd = vi.fn();
    const { getByRole } = render(MediaActions, {
      props: { mediaIds: ["m1", "m2"], appConfig: defaultAppConfig(), onAdd, onShare: vi.fn() },
    });
    await fireEvent.click(getByRole("button", { name: "Add to album" }));
    expect(onAdd).toHaveBeenCalledWith(["m1", "m2"]);
  });

  it("clicking Share invokes onShare with mediaIds", async () => {
    const onShare = vi.fn();
    const appConfig = await makeAppConfig(true);
    const { getByRole } = render(MediaActions, {
      props: { mediaIds: ["m1"], appConfig, onAdd: vi.fn(), onShare },
    });
    await fireEvent.click(getByRole("button", { name: "Share" }));
    expect(onShare).toHaveBeenCalledWith(["m1"]);
  });

  it("hides all buttons when mediaIds is empty (defense)", () => {
    const { queryByRole } = render(MediaActions, {
      props: { mediaIds: [], appConfig: defaultAppConfig(), onAdd: vi.fn(), onShare: vi.fn() },
    });
    expect(queryByRole("button", { name: "Add to album" })).toBeNull();
    expect(queryByRole("button", { name: "Share" })).toBeNull();
  });

  // --- Hide/Unhide button matrix ---

  it("shows Hide button in library context when hiddenConfigured=true", async () => {
    const appConfig = await makeAppConfig(true);
    const { getByRole, queryByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "library",
        hiddenConfigured: true,
        appConfig,
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onHide: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Hide" })).not.toBeNull();
    expect(queryByRole("button", { name: "Unhide" })).toBeNull();
    expect(queryByRole("button", { name: "Share" })).not.toBeNull();
  });

  it("shows Hide button in session context when hiddenConfigured=true", () => {
    const { getByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "session",
        hiddenConfigured: true,
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onHide: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Hide" })).not.toBeNull();
  });

  it("shows Hide button in album context when hiddenConfigured=true", () => {
    const { getByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "album",
        albumId: "a1",
        hiddenConfigured: true,
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onHide: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Hide" })).not.toBeNull();
  });

  it("shows Hide button in media-detail context when not hidden and hiddenConfigured=true", () => {
    const { getByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "media-detail",
        hiddenConfigured: true,
        isHidden: false,
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onHide: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Hide" })).not.toBeNull();
  });

  it("does NOT show Hide button when hiddenConfigured=false", () => {
    const { queryByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "library",
        hiddenConfigured: false,
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
      },
    });
    expect(queryByRole("button", { name: "Hide" })).toBeNull();
  });

  it("does NOT show Hide button when hiddenConfigured not set", () => {
    const { queryByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "library",
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
      },
    });
    expect(queryByRole("button", { name: "Hide" })).toBeNull();
  });

  it("hidden context: shows Unhide and Add, no Share, no Hide", () => {
    const { getByRole, queryByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "hidden",
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onUnhide: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Unhide" })).not.toBeNull();
    expect(getByRole("button", { name: "Add to album" })).not.toBeNull();
    expect(queryByRole("button", { name: "Share" })).toBeNull();
    expect(queryByRole("button", { name: "Hide" })).toBeNull();
  });

  it("media-detail hidden: shows Unhide and Add, no Share, no Hide", () => {
    const { getByRole, queryByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "media-detail",
        isHidden: true,
        hiddenConfigured: true,
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onUnhide: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Unhide" })).not.toBeNull();
    expect(getByRole("button", { name: "Add to album" })).not.toBeNull();
    expect(queryByRole("button", { name: "Share" })).toBeNull();
    expect(queryByRole("button", { name: "Hide" })).toBeNull();
  });

  it("clicking Hide invokes onHide with mediaIds", async () => {
    const onHide = vi.fn();
    const { getByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "library",
        hiddenConfigured: true,
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onHide,
      },
    });
    await fireEvent.click(getByRole("button", { name: "Hide" }));
    expect(onHide).toHaveBeenCalledWith(["m1"]);
  });

  it("clicking Unhide invokes onUnhide with mediaIds", async () => {
    const onUnhide = vi.fn();
    const { getByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "hidden",
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onUnhide,
      },
    });
    await fireEvent.click(getByRole("button", { name: "Unhide" }));
    expect(onUnhide).toHaveBeenCalledWith(["m1"]);
  });

  // Backwards-compat: no context prop behaves like library (no hide)
  it("no context prop: backward-compatible, shows Add and Share", async () => {
    const appConfig = await makeAppConfig(true);
    const { getByRole, queryByRole } = render(MediaActions, {
      props: { mediaIds: ["m1"], appConfig, onAdd: vi.fn(), onShare: vi.fn() },
    });
    expect(getByRole("button", { name: "Add to album" })).not.toBeNull();
    expect(getByRole("button", { name: "Share" })).not.toBeNull();
    expect(queryByRole("button", { name: "Hide" })).toBeNull();
    expect(queryByRole("button", { name: "Unhide" })).toBeNull();
  });

  // --- Sharing-enabled gate ---

  it("hides Share when appConfig.sharingEnabled is false (gate)", () => {
    const { queryByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        appConfig: defaultAppConfig(),
        onAdd: vi.fn(),
        onShare: vi.fn(),
      },
    });
    expect(queryByRole("button", { name: "Share" })).toBeNull();
    expect(queryByRole("button", { name: "Add to album" })).not.toBeNull();
  });

  it("shows Share when appConfig.sharingEnabled is true", async () => {
    const cfg = await makeAppConfig(true);
    const { getByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        appConfig: cfg,
        onAdd: vi.fn(),
        onShare: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Share" })).not.toBeNull();
  });
});
