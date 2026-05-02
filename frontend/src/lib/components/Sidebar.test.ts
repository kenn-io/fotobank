import { render } from "@testing-library/svelte";
import { describe, it, expect } from "vitest";
import Sidebar from "./Sidebar.svelte";
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
  // Defaults to sharingEnabled=false, ready=false. Useful when a test
  // doesn't care about the gate state — its initial ready=false is
  // safe because Sidebar reads sharingEnabled regardless of ready.
  const client = {
    GET: async () => ({ data: undefined, error: { status: 0 } }),
  } as unknown as Pick<Client, "GET">;
  return new AppConfigStore(client);
}

describe("Sidebar grouped entries", () => {
  it("renders BROWSE / CURATE / MANAGE group headers in order", async () => {
    const appConfig = await makeAppConfig(true);
    const { container } = render(Sidebar, {
      props: { active: "library", appConfig },
    });
    const headers = Array.from(container.querySelectorAll(".group-header"))
      .map((el) => el.textContent?.trim());
    expect(headers).toEqual(["BROWSE", "CURATE", "MANAGE"]);
  });

  it("renders Library + Sessions + Map + Hidden under BROWSE", () => {
    const { container } = render(Sidebar, {
      props: { active: "library", appConfig: defaultAppConfig() },
    });
    const browseGroup = container.querySelector(".group[data-group='browse']");
    expect(browseGroup?.textContent).toContain("Library");
    expect(browseGroup?.textContent).toContain("Sessions");
    expect(browseGroup?.textContent).toContain("Map");
    expect(browseGroup?.textContent).toContain("Hidden");
  });

  it("renders Albums under CURATE", () => {
    const { container } = render(Sidebar, {
      props: { active: "albums", appConfig: defaultAppConfig() },
    });
    const curate = container.querySelector(".group[data-group='curate']");
    expect(curate?.textContent).toContain("Albums");
  });

  it("renders Shares under MANAGE", async () => {
    const appConfig = await makeAppConfig(true);
    const { container } = render(Sidebar, {
      props: { active: "shares", appConfig },
    });
    const manage = container.querySelector(".group[data-group='manage']");
    expect(manage?.textContent).toContain("Shares");
  });

  it("highlights the active entry", () => {
    const { container } = render(Sidebar, {
      props: { active: "albums", appConfig: defaultAppConfig() },
    });
    const active = container.querySelector("a.active");
    expect(active?.textContent?.trim()).toBe("Albums");
  });

  it("highlights the hidden entry when active is 'hidden'", () => {
    const { container } = render(Sidebar, {
      props: { active: "hidden", appConfig: defaultAppConfig() },
    });
    const active = container.querySelector("a.active");
    expect(active?.textContent?.trim()).toBe("Hidden");
  });

  it("Hidden entry href points to /hidden", () => {
    const { container } = render(Sidebar, {
      props: { active: "library", appConfig: defaultAppConfig() },
    });
    const browseGroup = container.querySelector(".group[data-group='browse']");
    const hiddenLink = Array.from(browseGroup?.querySelectorAll("a") ?? []).find(
      (a) => a.textContent?.trim() === "Hidden",
    );
    expect(hiddenLink?.getAttribute("href")).toBe("/hidden");
  });
});

describe("Sidebar — sharing gate", () => {
  it("hides the Shares entry when appConfig.sharingEnabled is false", () => {
    const { queryByText, container } = render(Sidebar, {
      props: { active: "", appConfig: defaultAppConfig() },
    });
    expect(queryByText("Shares")).toBeNull();
    // The MANAGE group should not render either when its only entry is gated.
    expect(container.querySelector(".group[data-group='manage']")).toBeNull();
  });

  it("shows the Shares entry when sharingEnabled is true", async () => {
    const cfg = await makeAppConfig(true);
    const { findByText } = render(Sidebar, {
      props: { active: "", appConfig: cfg },
    });
    expect(await findByText("Shares")).toBeTruthy();
  });
});
