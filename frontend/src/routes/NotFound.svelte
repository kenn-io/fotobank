<!-- frontend/src/routes/NotFound.svelte -->
<script lang="ts">
  import { handleInternalLinkClick } from "../lib/router/router.svelte";
</script>

<div class="wrap">
  <h1>Page not found</h1>
  <p>That URL doesn't match any view in fotobank.</p>
  <a href="/library" onclick={(e) => handleInternalLinkClick(e, "/library")}>
    ← Back to Library
  </a>
</div>

<style>
  .wrap { padding: 32px; color: var(--ink); }
  h1 { margin: 0 0 8px; font-size: 18px; }
  p { color: var(--ink-2); margin: 0 0 16px; }
  a { color: var(--amber); text-decoration: none; }
  a:hover { text-decoration: underline; }
</style>
