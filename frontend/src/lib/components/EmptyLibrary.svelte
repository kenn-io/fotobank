<script lang="ts">
</script>

<section class="empty-library" aria-labelledby="empty-library-title">
  <h2 id="empty-library-title">Add photos to your library</h2>
  <p>Import a folder to start browsing your photos here. Fotobank copies the files into its store and leaves your source folder unchanged.</p>
  <p>Run this command on the machine running Fotobank, using the same account and configuration as the daemon:</p>
  <pre><code>fotobank import /path/to/photos --config /path/to/config.toml</code></pre>
  <p>Replace the example paths with your photo folder and configuration file. If someone else runs this library, ask them to import your photos.</p>
  <nav aria-label="Getting started">
    <a href="https://fotobank.ai/docs/guides/import/">Import guide</a>
    <a href="https://fotobank.ai/docs/guides/setup/">Setup guide</a>
  </nav>
</section>

<style>
  .empty-library {
    max-width: 38rem;
    padding: clamp(24px, 5vw, 64px) 24px;
    color: var(--text-secondary);
    line-height: 1.6;
  }
  h2 {
    margin: 0 0 var(--space-4);
    font-size: var(--font-size-2xl);
    color: var(--text-primary);
    line-height: 1.25;
  }
  p { margin: 0 0 var(--space-4); }
  pre {
    margin: 0 0 var(--space-4);
    padding: var(--space-3);
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  code { font-family: var(--font-mono); }
  nav { display: flex; flex-wrap: wrap; gap: var(--space-5); }
  a { color: var(--accent-blue); text-underline-offset: 3px; }
  a:focus-visible { outline: 2px solid var(--accent-blue); outline-offset: 4px; }
</style>
