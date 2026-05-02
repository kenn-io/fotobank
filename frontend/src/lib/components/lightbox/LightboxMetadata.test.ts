import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, waitFor } from "@testing-library/svelte";
import LightboxMetadata from "./LightboxMetadata.svelte";
import * as client from "../../ai/client";

vi.mock("../../ai/client", () => ({
  getMediaAIView: vi.fn(),
  retryPhotoAI: vi.fn(),
}));

const baseMedia = {
  id: "m1",
  timestamp: "2026-04-20T12:00:00Z",
  taken: new Date("2026-04-20T12:00:00Z"),
  aspect: 1,
  thumbUrl: "/g",
  thumbVersion: 0,
  original_filename: "IMG_001.JPG",
  size: 1024 * 1024 * 5,
  location_label: "Paris, France",
};

beforeEach(() => {
  vi.mocked(client.getMediaAIView).mockReset();
  vi.mocked(client.retryPhotoAI).mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("LightboxMetadata", () => {
  it("renders capture/location/download fields", () => {
    vi.mocked(client.getMediaAIView).mockResolvedValue({});
    const { getAllByText, getByText } = render(LightboxMetadata, {
      props: { media: baseMedia } as never,
    });
    // Filename appears twice (File row and Download link), so use getAllByText.
    expect(getAllByText("IMG_001.JPG").length).toBeGreaterThan(0);
    expect(getByText(/5\.0 MB/)).toBeTruthy();
    expect(getByText("Paris, France")).toBeTruthy();
  });

  it("mounts the AI section after the metadata fields", async () => {
    vi.mocked(client.getMediaAIView).mockResolvedValueOnce({
      tags: [{ key: "dog", label: "Dog", rank: 1 }],
    });
    const { getByText } = render(LightboxMetadata, {
      props: { media: baseMedia } as never,
    });
    await waitFor(() => expect(getByText("Dog")).toBeTruthy());
    expect(vi.mocked(client.getMediaAIView)).toHaveBeenCalledWith("m1");
  });

  it("renders the Search relevance row when score_components is supplied", () => {
    // V2 diagnostics-mode integration: when the search hit carries
    // score_components (the explain=true gated payload), the metadata
    // panel surfaces a Search relevance row that mirrors the
    // DiagnosticsBadge breakdown — RRF on the first line, BM25 with
    // its rank on the second, Vector with its rank on the third.
    vi.mocked(client.getMediaAIView).mockResolvedValue({});
    const scoreComponents = {
      rrf: 0.0156,
      bm25: 8.42,
      vector: 0.81,
      rank_bm25: 3,
      rank_vector: 7,
    };
    const { getByText, getByTestId } = render(LightboxMetadata, {
      props: { media: baseMedia, scoreComponents } as never,
    });
    expect(getByText("Search relevance")).toBeTruthy();
    const block = getByTestId("search-relevance");
    expect(block.textContent).toContain("RRF");
    expect(block.textContent).toContain("0.0156");
    expect(block.textContent).toContain("BM25");
    expect(block.textContent).toContain("8.42");
    expect(block.textContent).toContain("(rank 3)");
    expect(block.textContent).toContain("Vector");
    expect(block.textContent).toContain("0.81");
    expect(block.textContent).toContain("(rank 7)");
  });

  it("omits the Search relevance row when scoreComponents is not supplied", () => {
    // Existing callers (Library / Sessions / Albums lightbox) pass no
    // score_components — the row must not render at all so the
    // metadata panel stays unchanged for non-search contexts.
    vi.mocked(client.getMediaAIView).mockResolvedValue({});
    const { queryByText } = render(LightboxMetadata, {
      props: { media: baseMedia } as never,
    });
    expect(queryByText("Search relevance")).toBeNull();
  });
});
