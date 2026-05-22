import { render } from "@testing-library/svelte";
import { describe, it, expect } from "vitest";
import AlbumGrid from "./AlbumGrid.svelte";
import type { AlbumListItem } from "../albums/albumsStore.svelte";

function album(overrides: Partial<AlbumListItem> = {}): AlbumListItem {
  return {
    id: "a1",
    name: "Italy",
    item_count: 10,
    hidden_count: 0,
    created_at: "x",
    updated_at: "x",
    ...overrides,
  };
}

describe("AlbumGrid tile footer", () => {
  it("shows photo count without hidden suffix when hidden_count is 0", () => {
    const { container } = render(AlbumGrid, { props: { albums: [album()] } });
    const count = container.querySelector(".count");
    expect(count?.textContent).toBe("10 photos");
  });

  it("appends ' · N hidden' when hidden_count > 0", () => {
    const { container } = render(AlbumGrid, {
      props: { albums: [album({ item_count: 95, hidden_count: 5 })] },
    });
    const count = container.querySelector(".count");
    expect(count?.textContent).toBe("95 photos · 5 hidden");
  });

  it("uses singular 'photo' when item_count is 1", () => {
    const { container } = render(AlbumGrid, {
      props: { albums: [album({ item_count: 1, hidden_count: 0 })] },
    });
    const count = container.querySelector(".count");
    expect(count?.textContent).toBe("1 photo");
  });

  it("does not show hidden suffix when hidden_count is absent", () => {
    const a: AlbumListItem = {
      id: "a1",
      name: "A",
      item_count: 3,
      created_at: "x",
      updated_at: "x",
    };
    const { container } = render(AlbumGrid, { props: { albums: [a] } });
    const count = container.querySelector(".count");
    expect(count?.textContent).not.toContain("hidden");
  });
});
