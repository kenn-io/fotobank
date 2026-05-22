<!-- frontend/src/lib/components/lightbox/LightboxMedia.svelte -->
<!--
  Branches between LightboxImage and LightboxVideo based on `kind`.
  We use the `onReady` callback pattern (not `bind:this` exposing
  imperative methods) because Svelte 5 runes mode doesn't allow
  `export function`. The Lightbox parent (T16) passes `onReady` so it
  can stash the LightboxImageApi for keyboard shortcuts.
-->
<script lang="ts">
  import LightboxImage from "./LightboxImage.svelte";
  import type { LightboxImageApi } from "./LightboxImage.svelte";
  import LightboxVideo from "./LightboxVideo.svelte";

  let {
    kind,
    src,
    alt,
    poster,
    onError,
    onReady,
  }: {
    kind: "image" | "video";
    src: string;
    alt: string;
    poster?: string | undefined;
    onError?: (() => void) | undefined;
    onReady?: ((api: LightboxImageApi) => void) | undefined;
  } = $props();
</script>

{#if kind === "video"}
  <LightboxVideo {src} {poster} {onError} />
{:else}
  <LightboxImage {src} {alt} {onError} {onReady} />
{/if}
