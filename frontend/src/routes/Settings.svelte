<script lang="ts">
  import { handleInternalLinkClick } from "../lib/router/router.svelte";
</script>

<section class="settings">
  <h1>Settings</h1>
  <p class="intro">Manage photo privacy and optional AI processing.</p>
  <div class="setting">
    <a href="/settings/ai" onclick={(event) => handleInternalLinkClick(event, "/settings/ai")}>AI processing</a>
    <p>Check processing status and manage tagging, captions, and image search.</p>
  </div>
  <div class="setting">
    <a href="/hidden" onclick={(event) => handleInternalLinkClick(event, "/hidden")}>Hidden photos</a>
    <p>Unlock hidden photos with your passcode.</p>
  </div>

  <h2>Workflow help</h2>
  <nav aria-label="Workflow help">
    <a href="https://fotobank.kenn.io/docs/guides/import/">Import photos</a>
    <a href="https://fotobank.kenn.io/docs/guides/checkouts/">Edit files in a checkout</a>
    <a href="https://fotobank.kenn.io/docs/guides/backup/">Back up and restore</a>
  </nav>
  <p class="host-note">Storage paths and daemon settings are managed on the machine running Fotobank, not on this page. See the <a href="https://fotobank.kenn.io/docs/guides/setup/">setup guide</a>, or ask the person who runs your library.</p>
</section>

<style>
  .settings { max-width: 48rem; padding: 24px; color: var(--text-secondary); line-height: 1.6; }
  h1 { margin: 0 0 8px; color: var(--text-primary); font-size: var(--font-size-2xl); }
  .intro { margin: 0 0 24px; }
  .setting { padding: 16px 0; border-top: 1px solid var(--border-default); }
  .setting p { margin: 4px 0 0; }
  .setting a { font-size: var(--font-size-lg); }
  h2 { margin: 32px 0 8px; color: var(--text-primary); font-size: var(--font-size-lg); }
  nav { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; }
  nav a { padding: 8px 0; }
  a { color: var(--accent-blue); text-underline-offset: 3px; }
  a:focus-visible { outline: 2px solid var(--accent-blue); outline-offset: 4px; }
  .host-note { margin: 24px 0 0; }
</style>
