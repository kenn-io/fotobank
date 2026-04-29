<script lang="ts">
  let {
    mediaIds,
    context = "library",
    albumId,
    onAdd,
    onShare,
    onRemove,
  }: {
    mediaIds: string[];
    context?: "library" | "session" | "album";
    albumId?: string;
    onAdd: (ids: string[]) => void;
    onShare: (ids: string[]) => void;
    onRemove?: (ids: string[]) => void;
  } = $props();
</script>

{#if mediaIds.length > 0}
  <div class="media-actions">
    <button type="button" onclick={() => onAdd(mediaIds)}>Add to album</button>
    <button type="button" onclick={() => onShare(mediaIds)}>Share</button>
    {#if context === "album" && onRemove && albumId}
      <button type="button" class="danger" onclick={() => onRemove(mediaIds)}>
        Remove from this album
      </button>
    {/if}
  </div>
{/if}

<style>
  .media-actions { display: flex; gap: 8px; }
  .danger { color: var(--danger); border-color: var(--danger); }
</style>
