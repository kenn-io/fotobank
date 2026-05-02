<!-- frontend/src/lib/components/lightbox/LightboxActions.svelte -->
<script lang="ts">
  import MediaActions from "../MediaActions.svelte";
  import type { MediaStore, Media } from "../../media/mediaStore.svelte";
  import type { AlbumsStore } from "../../albums/albumsStore.svelte";
  import type { HiddenStore } from "../../hidden/hiddenStore.svelte";
  import type { ToastStore } from "../../toasts/toastStore.svelte";
  import type { AppConfigStore } from "../../app/appConfig.svelte";
  import type { LightboxSource } from "../../lightbox/lightboxSession.svelte";
  import { lightboxSession } from "../../lightbox/lightboxSession.svelte";

  let {
    source,
    media,
    rawMedia,
    mediaStore,
    albumsStore,
    hiddenStore,
    toastStore,
    appConfig,
    onAdd,
    onShare,
    onDone,
  }: {
    source: LightboxSource;
    media: Media;
    rawMedia: Record<string, unknown> | null;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
    appConfig: AppConfigStore;
    onAdd: (ids: string[]) => void;
    onShare: (ids: string[]) => void;
    /** Called after a successful hide/unhide — Lightbox parent advances or closes. */
    onDone: (op: "hide" | "unhide", succeeded: string[]) => void;
  } = $props();

  const isUnhideContext = $derived(source.kind === "hidden");

  async function onHide(ids: string[]): Promise<void> {
    if (!window.confirm("Hide this photo?")) return;
    let result;
    try {
      result = await hiddenStore.hide(ids);
    } catch {
      toastStore.push({ message: "Hide failed. Try again.", kind: "error" });
      return;
    }
    const succeeded = result.succeeded ?? [];
    if (succeeded.length > 0) {
      // Notify the parent FIRST so its onActionDone can capture the
      // advance target while `nav` still reflects pre-mutation state.
      // If we pruned navIds first, nav.nextId/prevId would resolve to
      // null (active id no longer in navIds) and the lightbox would
      // close instead of advancing.
      onDone("hide", succeeded);
      // Snapshot mutation — safe to run after navigation; LightboxActions
      // is unmounted/remounted with the new active media by then, but
      // removeIds operates on the underlying store, not on us.
      lightboxSession.removeIds(succeeded);
      // Durable state per source kind. Exhaustive switch over
      // LightboxSource so a future kind addition trips the type
      // checker via the `never`-defaulted else.
      switch (source.kind) {
        case "library":
        case "sessions":
        case "search":
          // Hide from a search-context lightbox: the hidden row drops
          // out of the visible mediaStore for any other surface that
          // shares the cache. The search store's own results array
          // ages out on the next refetch (a hidden row no longer
          // matches include_hidden=false); we don't proactively prune
          // it here because the search route may be unmounted by the
          // time hide resolves.
          mediaStore.removeMany(succeeded);
          break;
        case "album":
          albumsStore.markStale();
          break;
        case "hidden":
          // Hide button is suppressed via isUnhideContext when source
          // is hidden; this branch is unreachable today. Kept here so
          // the switch is exhaustive — if the gating ever changes the
          // code stays correct without silently no-op'ing durable state.
          break;
        default: {
          const _exhaustive: never = source;
          void _exhaustive;
        }
      }
    }
    const failed = result.failed ?? [];
    if (failed.length > 0) {
      toastStore.push({
        message: "Could not hide this photo.",
        details: failed.map((f) => `${f.id}: ${f.code}`),
        kind: "error",
      });
    }
  }

  async function onUnhide(ids: string[]): Promise<void> {
    if (!window.confirm("Unhide this photo?")) return;
    let result;
    try {
      result = await hiddenStore.unhide(ids);
    } catch {
      toastStore.push({ message: "Unhide failed. Try again.", kind: "error" });
      return;
    }
    const succeeded = result.succeeded ?? [];
    if (succeeded.length > 0) {
      // Notify the parent FIRST so its onActionDone can capture the
      // advance target before navIds is pruned. See onHide for the
      // detailed rationale.
      onDone("unhide", succeeded);
      // Snapshot mutation (removes from hidden navIds)
      lightboxSession.removeIds(succeeded);
      // Re-introduce the row to visible mediaStore so /library shows it.
      const raw = rawMedia ?? { id: media.id, thumb_version: media.thumbVersion, width: 1, height: 1 };
      mediaStore.mergeRaw([{ ...raw, hidden_at: null }]);
      // Albums stale (counts may shift)
      albumsStore.markStale();
    }
    const failed = result.failed ?? [];
    if (failed.length > 0) {
      toastStore.push({
        message: "Could not unhide this photo.",
        details: failed.map((f) => `${f.id}: ${f.code}`),
        kind: "error",
      });
    }
  }
</script>

<MediaActions
  mediaIds={[media.id]}
  context="media-detail"
  isHidden={isUnhideContext}
  hiddenConfigured={hiddenStore.configured}
  {appConfig}
  {onAdd}
  {onShare}
  {onHide}
  {onUnhide}
/>
