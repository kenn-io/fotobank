<script lang="ts">
  import type { AppConfigStore } from "../app/appConfig.svelte";

  let {
    mediaIds,
    context = "library",
    albumId,
    hiddenConfigured = false,
    isHidden = false,
    appConfig,
    onAdd,
    onShare,
    onRemove,
    onHide,
    onUnhide,
  }: {
    mediaIds: string[];
    context?: "library" | "session" | "album" | "media-detail" | "hidden";
    albumId?: string;
    hiddenConfigured?: boolean;
    isHidden?: boolean;
    appConfig: AppConfigStore;
    onAdd: (ids: string[]) => void;
    onShare: (ids: string[]) => void;
    onRemove?: (ids: string[]) => void;
    onHide?: (ids: string[]) => void;
    onUnhide?: (ids: string[]) => void;
  } = $props();

  // Derived flags for button visibility.
  //
  // Unhide context: either explicitly in "hidden" route, or media-detail
  // when the media row is hidden.
  const isUnhideContext = $derived(
    context === "hidden" || (context === "media-detail" && isHidden),
  );

  // Show Share only outside the unhide context AND when sharing UI is enabled.
  const showShare = $derived(!isUnhideContext && appConfig.sharingEnabled);

  // Show Hide when hiddenConfigured and we're not in unhide context.
  const showHide = $derived(hiddenConfigured && !isUnhideContext);
</script>

{#if mediaIds.length > 0}
  <div class="media-actions">
    <button type="button" onclick={() => onAdd(mediaIds)}>Add to album</button>

    {#if showShare}
      <button type="button" onclick={() => onShare(mediaIds)}>Share</button>
    {/if}

    {#if isUnhideContext && onUnhide}
      <button type="button" onclick={() => onUnhide!(mediaIds)}>Unhide</button>
    {/if}

    {#if showHide && onHide}
      <button type="button" onclick={() => onHide!(mediaIds)}>Hide</button>
    {/if}

    {#if context === "album" && onRemove && albumId}
      <button type="button" class="danger" onclick={() => onRemove!(mediaIds)}>
        Remove from this album
      </button>
    {/if}
  </div>
{/if}

<style>
  .media-actions { display: flex; gap: 8px; }
  .danger { color: var(--accent-red); border-color: var(--accent-red); }
</style>
