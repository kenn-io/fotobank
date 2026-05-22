import { render } from "@testing-library/svelte";
import { describe, it, expect } from "vitest";
import Sidebar from "./Sidebar.svelte";
import { AppConfigStore } from "../app/appConfig.svelte";
import type { Client } from "../api/client";
import type { ActiveFilters } from "../filters/activeFilters";

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

// Filter props are required since SF-16, but the BROWSE/CURATE/MANAGE
// nav assertions are independent of them — every test in this file
// passes the same dummy filter set unless it cares about FILTERS
// specifically. emptyFilters is a factory (see activeFilters.ts) so
// each test gets a fresh array; the test helper mirrors that.
function emptyFilters(): ActiveFilters {
  return { cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null };
}

const navProps = {
  route: "albums",
  activeFilters: emptyFilters(),
  facetsResponse: null,
  onFiltersChange: () => {},
};

describe("Sidebar grouped entries", () => {
  it("renders BROWSE / CURATE / MANAGE group headers in order", async () => {
    const appConfig = await makeAppConfig(true);
    const { container } = render(Sidebar, {
      props: { active: "library", appConfig, ...navProps },
    });
    const headers = Array.from(container.querySelectorAll(".group-header"))
      .map((el) => el.textContent?.trim());
    expect(headers).toEqual(["BROWSE", "CURATE", "MANAGE"]);
  });

  it("renders Library + Sessions + Map + Hidden under BROWSE", () => {
    const { container } = render(Sidebar, {
      props: { active: "library", appConfig: defaultAppConfig(), ...navProps },
    });
    const browseGroup = container.querySelector(".group[data-group='browse']");
    expect(browseGroup?.textContent).toContain("Library");
    expect(browseGroup?.textContent).toContain("Sessions");
    expect(browseGroup?.textContent).toContain("Map");
    expect(browseGroup?.textContent).toContain("Hidden");
  });

  it("renders Albums under CURATE", () => {
    const { container } = render(Sidebar, {
      props: { active: "albums", appConfig: defaultAppConfig(), ...navProps },
    });
    const curate = container.querySelector(".group[data-group='curate']");
    expect(curate?.textContent).toContain("Albums");
  });

  it("renders Shares under MANAGE", async () => {
    const appConfig = await makeAppConfig(true);
    const { container } = render(Sidebar, {
      props: { active: "shares", appConfig, ...navProps },
    });
    const manage = container.querySelector(".group[data-group='manage']");
    expect(manage?.textContent).toContain("Shares");
  });

  it("highlights the active entry", () => {
    const { container } = render(Sidebar, {
      props: { active: "albums", appConfig: defaultAppConfig(), ...navProps },
    });
    const active = container.querySelector("a.active");
    expect(active?.textContent?.trim()).toBe("Albums");
  });

  it("highlights the hidden entry when active is 'hidden'", () => {
    const { container } = render(Sidebar, {
      props: { active: "hidden", appConfig: defaultAppConfig(), ...navProps },
    });
    const active = container.querySelector("a.active");
    expect(active?.textContent?.trim()).toBe("Hidden");
  });

  it("Hidden entry href points to /hidden", () => {
    const { container } = render(Sidebar, {
      props: { active: "library", appConfig: defaultAppConfig(), ...navProps },
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
      props: { active: "", appConfig: defaultAppConfig(), ...navProps },
    });
    expect(queryByText("Shares")).toBeNull();
    // The MANAGE group should not render either when its only entry is gated.
    expect(container.querySelector(".group[data-group='manage']")).toBeNull();
  });

  it("shows the Shares entry when sharingEnabled is true", async () => {
    const cfg = await makeAppConfig(true);
    const { findByText } = render(Sidebar, {
      props: { active: "", appConfig: cfg, ...navProps },
    });
    expect(await findByText("Shares")).toBeTruthy();
  });
});

describe("Sidebar — FILTERS group", () => {
  it("does NOT render FILTERS on /albums (only library/search/map)", () => {
    const { queryByText } = render(Sidebar, {
      props: {
        active: "albums",
        appConfig: defaultAppConfig(),
        ...navProps,
        route: "albums",
      },
    });
    expect(queryByText("FILTERS")).toBeNull();
  });

  it("renders FILTERS on /library", () => {
    const { getByText } = render(Sidebar, {
      props: {
        active: "library",
        appConfig: defaultAppConfig(),
        ...navProps,
        route: "library",
      },
    });
    expect(getByText("FILTERS")).toBeTruthy();
  });

  it("renders FILTERS on /search", () => {
    const { getByText } = render(Sidebar, {
      props: {
        active: "",
        appConfig: defaultAppConfig(),
        ...navProps,
        route: "search",
      },
    });
    expect(getByText("FILTERS")).toBeTruthy();
  });

  it("renders FILTERS on /map", () => {
    const { getByText } = render(Sidebar, {
      props: {
        active: "map",
        appConfig: defaultAppConfig(),
        ...navProps,
        route: "map",
      },
    });
    expect(getByText("FILTERS")).toBeTruthy();
  });
});
