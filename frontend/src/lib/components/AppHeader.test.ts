import { describe, it, expect, beforeEach } from "vitest";
import { render } from "@testing-library/svelte";
import AppHeader from "./AppHeader.svelte";
import { router } from "../router/router.svelte";
import type { Principal } from "../app/appConfig.svelte";

// Reset URL between tests so each case starts at a known route. This
// also resets the router's `current` to "library" via syncFromLocation.
function setLocation(pathname: string) {
  window.history.replaceState({}, "", pathname);
  router.syncFromLocation();
}

const principal: Principal = {
  hub: "dev-local",
  userId: "owner",
  handle: "owner",
};

function renderHeader(overrides: Record<string, unknown> = {}) {
  return render(AppHeader, {
    props: {
      principal,
      ready: true,
      onsearch: () => {},
      ...overrides,
    },
  });
}

describe("AppHeader", () => {
  beforeEach(() => {
    setLocation("/");
  });

  it("renders the fotobank brand mark", () => {
    const { container } = renderHeader();
    const brand = container.querySelector(".brand");
    expect(brand).not.toBeNull();
    expect(brand?.textContent).toContain("fotobank");
  });

  it("renders four nav tabs and does NOT include Search", () => {
    const { container } = renderHeader();
    const links = Array.from(container.querySelectorAll("nav.tabs a"));
    const labels = links.map((a) => a.textContent?.trim() ?? "");
    expect(labels).toEqual(["Library", "Map", "Albums", "Hidden"]);
    expect(labels).not.toContain("Search");
  });

  it("highlights the Library tab on /library", () => {
    setLocation("/library");
    const { container } = renderHeader();
    const links = container.querySelectorAll("nav.tabs a");
    // Library is the first tab; it should be the only one with .active.
    expect(links[0]?.classList.contains("active")).toBe(true);
    for (let i = 1; i < links.length; i += 1) {
      expect(links[i]?.classList.contains("active")).toBe(false);
    }
  });

  it("exposes the search input via data-testid", () => {
    const { getByTestId } = renderHeader();
    const input = getByTestId("search-input") as HTMLInputElement;
    expect(input).toBeTruthy();
    expect(input.tagName).toBe("INPUT");
    expect(input.type).toBe("search");
  });

  it("mounts identity chips when ready=true", () => {
    const { getByTestId } = renderHeader({ ready: true });
    expect(getByTestId("id-chip-hub")).toBeTruthy();
    expect(getByTestId("id-chip-user")).toBeTruthy();
  });

  it("does NOT render identity chips while ready=false", () => {
    const { queryByTestId } = renderHeader({ ready: false });
    expect(queryByTestId("id-chip-hub")).toBeNull();
    expect(queryByTestId("id-chip-user")).toBeNull();
  });
});
