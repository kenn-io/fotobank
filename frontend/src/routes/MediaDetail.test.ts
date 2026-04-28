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
});
