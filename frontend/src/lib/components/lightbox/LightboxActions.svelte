<!-- frontend/src/lib/components/lightbox/LightboxActions.svelte -->
<script lang="ts">
  import MediaActions from "../MediaActions.svelte";
  import type { MediaStore, Media } from "../../media/mediaStore.svelte";
  import type { AlbumsStore } from "../../albums/albumsStore.svelte";
  import type { HiddenStore } from "../../hidden/hiddenStore.svelte";
  import type { ToastStore } from "../../toasts/toastStore.svelte";
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
      // 1. Snapshot mutation
      lightboxSession.removeIds(succeeded);
      // 2. Durable state per source kind. Exhaustive switch over
      // LightboxSource so a future kind addition trips the type
      // checker via the `never`-defaulted else.
      switch (source.kind) {
        case "library":
        case "sessions":
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
      onDone("hide", succeeded);
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
      // 1. Snapshot mutation (removes from hidden navIds)
      lightboxSession.removeIds(succeeded);
      // 2. Re-introduce the row to visible mediaStore so /library shows it.
      const raw = rawMedia ?? { id: media.id, thumb_version: media.thumbVersion, width: 1, height: 1 };
      mediaStore.mergeRaw([{ ...raw, hidden_at: null }]);
      // 3. Albums stale (counts may shift)
      albumsStore.markStale();
      onDone("unhide", succeeded);
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
  {onAdd}
  {onShare}
  {onHide}
  {onUnhide}
/>
