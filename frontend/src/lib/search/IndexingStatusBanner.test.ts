import { describe, it, expect } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import { flushSync, tick } from "svelte";
import IndexingStatusBanner from "./IndexingStatusBanner.svelte";

// IndexingStatusBanner is the search-page banner that explains why the
// engine fell back to BM25. The component surfaces three orthogonal
// banners, evaluated in priority order via the {#if/:else if} chain:
//
//   1. under-80% — a "still indexing" hint shown only while the user
//      has typed a query and the library hasn't reached the activator
//      threshold (semanticUnavailable=false; the engine is hybrid-
//      ranking what it has). Suppressed once semanticUnavailable=true
//      so the more specific banners take precedence.
//   2. no_active_generation — semantic search has never been activated
//      for this library. Dismissable per session via a local state
//      flag; remounting the component resets the dismissal (per-page
//      navigation is implicitly per-session).
//   3. query_embedding_failed — the engine had an active generation
//      but the per-query embedding call failed for the latest request.
//      Auto-dismisses on the next successful query because `reason`
//      transitions back to "" — there is no per-component dismiss
//      state to reset.

describe("IndexingStatusBanner", () => {
  it("under-80% banner shows when q != '' && completeness < 0.80 && !semanticUnavailable", () => {
    // Plan acceptance: the 80% threshold matches the activator's
    // EmbeddingThreshold (the search engine's hybrid pivot). Below it,
    // hybrid ranking covers a partial library — the banner surfaces
    // that to the user so they understand why some results "feel"
    // weaker. The banner is only relevant while the user has a query;
    // an empty-query view doesn't need the explanation.
    const { container, getByText } = render(IndexingStatusBanner, {
      props: {
        completeness: 0.5,
        semanticUnavailable: false,
        reason: "",
        hasQuery: true,
      },
    });
    expect(container.querySelector(".banner")).not.toBeNull();
    // Body mentions the rounded percentage so the user has a concrete
    // sense of how much of the library is covered.
    expect(getByText(/50%/)).toBeTruthy();
  });

  it("under-80% banner does NOT show when query is empty", () => {
    // Without a query the banner is noise — there's nothing being
    // ranked. Tests the (hasQuery=false) branch of the showUnderEighty
    // $derived guard.
    const { container } = render(IndexingStatusBanner, {
      props: {
        completeness: 0.5,
        semanticUnavailable: false,
        reason: "",
        hasQuery: false,
      },
    });
    expect(container.querySelector(".banner")).toBeNull();
  });

  it("under-80% banner does NOT show when semanticUnavailable=true", () => {
    // When semantic is fully unavailable the no_active_generation /
    // query_embedding_failed banners take over. The under-80% banner's
    // !semanticUnavailable guard ensures the more specific message
    // wins.
    const { container } = render(IndexingStatusBanner, {
      props: {
        completeness: 0.5,
        semanticUnavailable: true,
        reason: "",
        hasQuery: true,
      },
    });
    expect(container.querySelector(".banner")).toBeNull();
  });

  it("no_active_generation banner is dismissable per session", async () => {
    // The dismiss button flips local `dismissed` $state which gates
    // the banner. After clicking, the banner disappears and stays
    // dismissed for the lifetime of the component. Plan calls this
    // "per-session" because remounting the component resets the
    // local state — the user gets a fresh banner if they navigate
    // away and back, but not while the page is mounted.
    const { container, getByText } = render(IndexingStatusBanner, {
      props: {
        completeness: 0,
        semanticUnavailable: true,
        reason: "no_active_generation",
        hasQuery: false,
      },
    });
    expect(container.querySelector(".banner")).not.toBeNull();
    const dismiss = getByText("Dismiss");
    await fireEvent.click(dismiss);
    flushSync();
    await tick();
    expect(container.querySelector(".banner")).toBeNull();
  });

  it("query_embedding_failed banner auto-dismisses when reason transitions back to ''", async () => {
    // The banner has no per-component dismiss for this branch — the
    // showQueryEmbedFailed $derived only renders when reason is
    // "query_embedding_failed". The next successful query response
    // carries reason="", which flips the $derived false and the banner
    // disappears. The test patches the props via $set to drive the
    // transition.
    const { container, rerender } = render(IndexingStatusBanner, {
      props: {
        completeness: 1,
        semanticUnavailable: true,
        reason: "query_embedding_failed",
        hasQuery: true,
      },
    });
    expect(container.querySelector(".banner")).not.toBeNull();
    await rerender({
      completeness: 1,
      semanticUnavailable: false,
      reason: "",
      hasQuery: true,
    });
    flushSync();
    await tick();
    expect(container.querySelector(".banner")).toBeNull();
  });
});
