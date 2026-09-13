# Maintaining the documentation

Edit the source pages, then build the complete site to check their links.
Follow the writing standard in `AGENTS.md` when changing prose.

## Where should a change go?

| Reader's question | Owning source |
| --- | --- |
| Why does Fotobank exist? | `website/index.html` and `website/index.md` |
| What should I know before using it? | `website/guide/index.html` and `website/guide.md` |
| How do I perform a task? | `docs/guides/` |
| How does the implementation work? | `docs/architecture/` |
| Where do I start reading? | Root `README.md`, `docs/index.md`, and `website/llms.txt` |

Keep each HTML page and its Markdown companion in sync. Keep detailed rules in
their owning guide and link to them from introductions and indexes. Put proposed
work in kata, not in the documentation for current behavior.

## How is the site published?

`zensical.toml` defines the technical documentation navigation. The build puts
those pages under `site/docs/`, copies `website/` to `site/`, and adds the shared
fonts. `scripts/docs/build.mjs` owns that generated output. Do not edit `site/`.

The website and guides describe the source revision being built. When describing
a release, check its capabilities against that release rather than newer `main`
code.

## How do I check a change?

1. Run `make docs-check` to build the full site and validate its links.
2. Run `make docs-serve` to view it locally. The command prints the address.
3. Read the changed pages in the browser. Check narrow layouts when changing
   website copy or markup.

A successful build does not verify capability claims. Check commands, defaults,
authorization rules, and failure behavior against the code. Keep aspirations
separate from features that work today.
