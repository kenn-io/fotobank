<!-- frontend/src/routes/MediaDetail.svelte -->
<script lang="ts">
  let { id }: { id: string } = $props();
</script>

<div class="wrap">
  <a href="/library" class="back" aria-label="Back to library">←</a>
  <img src={`/api/v1/media/${encodeURIComponent(id)}/thumb?size=preview&v=0`} alt="" />
  <p class="note">F2 will replace this with the full lightbox.</p>
</div>

<style>
  .wrap {
    background: black;
    min-height: 100vh;
    display: grid;
    place-items: center;
    color: #ccc;
    position: relative;
  }
  .back {
    position: absolute;
    top: 12px; left: 12px;
    color: #ccc; text-decoration: none;
    font-size: 24px;
  }
  .wrap img { max-width: 90vw; max-height: 80vh; object-fit: contain; }
  .note { font-size: 11px; color: #888; }
</style>
