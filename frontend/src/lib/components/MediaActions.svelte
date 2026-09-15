<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
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
    <Button onclick={() => onAdd(mediaIds)}>Add to album</Button>

    {#if showShare}
      <Button onclick={() => onShare(mediaIds)}>Share</Button>
    {/if}

    {#if isUnhideContext && onUnhide}
      <Button onclick={() => onUnhide!(mediaIds)}>Unhide</Button>
    {/if}

    {#if showHide && onHide}
      <Button onclick={() => onHide!(mediaIds)}>Hide</Button>
    {/if}

    {#if context === "album" && onRemove && albumId}
      <Button tone="danger" onclick={() => onRemove!(mediaIds)}>
        Remove from this album
      </Button>
    {/if}
  </div>
{/if}

<style>
  .media-actions { display: flex; flex-wrap: wrap; gap: 8px; }
  @media (max-width: 760px) {
    .media-actions { --kit-control-height: 44px; }
  }
</style>
