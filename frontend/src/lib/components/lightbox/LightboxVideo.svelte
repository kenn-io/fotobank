<!-- frontend/src/lib/components/lightbox/LightboxVideo.svelte -->
<script lang="ts">
  let {
    src,
    poster,
    onError,
  }: {
    src: string;
    poster?: string | undefined;
    onError?: (() => void) | undefined;
  } = $props();
</script>

<div class="lb-video-container">
  <!-- svelte-ignore a11y_media_has_caption -->
  <video
    {src}
    {poster}
    controls
    onerror={() => onError?.()}
  ></video>
</div>

<style>
  .lb-video-container {
    width: 100%; height: 100%;
    display: flex; align-items: center; justify-content: center;
  }
  .lb-video-container video {
    max-width: 100%; max-height: 100%;
  }
</style>
