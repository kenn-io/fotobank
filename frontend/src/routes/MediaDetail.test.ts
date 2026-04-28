import { describe, it, expect, vi } from "vitest";
import { render } from "@testing-library/svelte";
import MediaDetail from "./MediaDetail.svelte";
import { MediaStore } from "../lib/media/mediaStore.svelte";

function storeWith(raw: Record<string, unknown>): MediaStore {
  const s = new MediaStore({ GET: vi.fn() } as never);
  s.mergeRaw([raw]);
  return s;
}

describe("MediaDetail", () => {
  const baseRaw = {
    id: "abc-123",
    timestamp: "2024-06-15T14:30:22Z",
    width: 3,
    height: 2,
    thumb_version: 3,
  };

  it("renders Location row when location_label is set", () => {
    const store = storeWith({
      ...baseRaw,
      latitude: 48.8566,
      longitude: 2.3522,
      location_label: "Paris, Île-de-France, France",
    });
    const { getByText } = render(MediaDetail, {
      props: { id: "abc-123", mediaStore: store },
    });
    expect(getByText("Location")).toBeTruthy();
    expect(getByText("Paris, Île-de-France, France")).toBeTruthy();
    expect(getByText("48.8566° N, 2.3522° E")).toBeTruthy();
  });

  it("renders coords-only when label is absent", () => {
    const store = storeWith({ ...baseRaw, latitude: 48.8566, longitude: 2.3522 });
    const { getByText, queryByText } = render(MediaDetail, {
      props: { id: "abc-123", mediaStore: store },
    });
    expect(getByText("Location")).toBeTruthy();
    expect(getByText("48.8566° N, 2.3522° E")).toBeTruthy();
    expect(queryByText(/Paris/)).toBeNull();
  });

  it("renders no Location row when neither label nor coords", () => {
    const store = storeWith(baseRaw);
    const { queryByText } = render(MediaDetail, {
      props: { id: "abc-123", mediaStore: store },
    });
    expect(queryByText("Location")).toBeNull();
  });

  it("re-fetches when id prop changes", async () => {
    // App.svelte mounts MediaDetail without a {#key} wrapper, so
    // navigating from one /media/:id to another reuses this component
    // instance. The id-keyed $effect must re-fetch the new (uncached)
    // row; the previous onMount-only path would have left the second
    // navigation stuck on Loading… Build the response body from the
    // requested URL so each id caches under its own key — otherwise
    // the second render would hit the store and short-circuit the
    // effect, masking the regression we're guarding against.
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation((input) => {
        const url = typeof input === "string" ? input : (input as Request).url;
        const id = url.split("/").pop() ?? "unknown";
        return Promise.resolve(
          new Response(
            JSON.stringify({
              id,
              timestamp: "2024-06-15T14:30:22Z",
              width: 1,
              height: 1,
              thumb_version: 1,
            }),
            { status: 200 },
          ),
        );
      });
    const store = new MediaStore({ GET: vi.fn() } as never);
    // First nav: id=first, no cached row → triggers fetch.
    const first = render(MediaDetail, {
      props: { id: "first", mediaStore: store },
    });
    // Wait for the first fetch to settle.
    await new Promise((r) => setTimeout(r, 0));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/media/first");
    fetchMock.mockClear();
    // Second nav: id=second, also uncached → must trigger another fetch.
    await first.rerender({ id: "second", mediaStore: store });
    await new Promise((r) => setTimeout(r, 0));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/media/second");
    fetchMock.mockRestore();
  });
});
