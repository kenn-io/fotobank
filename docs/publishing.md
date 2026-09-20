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

The canonical website is `https://fotobank.ai/`, with documentation under
`https://fotobank.ai/docs/`. Use this domain in site metadata and public links.

`zensical.toml` defines the technical documentation navigation. The build puts
those pages under `site/docs/`, copies `website/` to `site/`, and adds the shared
fonts. `scripts/docs/build.mjs` owns that generated output. Do not edit `site/`.

The website and guides describe the source revision being built. When describing
a release, check its capabilities against that release rather than newer `main`
code.

## How do I deploy the website?

An operator runs deployments manually. Publishing the website does not publish
the source repository or create a software release. Git pushes do not trigger
Vercel deployments.

From the repository root, install the build tools and sign in to Vercel:

```bash
mise install
mise exec -- bunx vercel@58.4.4 login
```

On a headless machine, open the printed sign-in URL in your laptop's browser.
This signs the CLI into Vercel; a GitHub CLI login is not a substitute.

Link this checkout once:

```bash
make docs-link
```

Choose the team that owns the site and select or create the `fotobank` project.
Use `./` as the project directory. Do not connect a Git repository. The local
link lives in `.vercel/`, which Git ignores. Keep the Vercel framework preset
at **Other**; the repository configuration disables remote installation and
build commands because the site is built locally.

To publish, including each later update:

```bash
make docs-deploy
```

This rebuilds and checks the site before deploying to the linked project's
production environment. The [Vercel upload allowlist](https://vercel.com/docs/deployments/vercel-ignore)
includes only `site/` and `vercel.json`. It excludes application source, Git
history, and local project credentials. Do not run a bare deployment command
against an old `site/` directory.

For the first deployment, open the project's **Settings → Domains**, add
`fotobank.ai`, and apply the DNS records Vercel displays at your DNS provider.
Do not replace unrelated mail or verification records. The deployment command
does not change DNS. Once Vercel reports the domain ready, open
`https://fotobank.ai/`, `/guide/`, `/docs/`, and `/llms.txt` to check the live site.

After linking the project, inspect the upload without publishing anything:

```bash
make docs-check
mise exec -- bunx vercel@58.4.4 deploy --dry
```

## How do I check a change?

1. Run `make docs-check` to build the full site and validate its links.
2. Run `make docs-serve` to view it locally. The command prints the address.
3. Read the changed pages in the browser. Check narrow layouts when changing
   website copy or markup.

A successful build does not verify capability claims. Check commands, defaults,
authorization rules, and failure behavior against the code. Keep aspirations
separate from features that work today.

## Which screenshots belong in the docs?

Keep an image only when it helps readers understand the current product or
complete a task. Architecture pages explain ownership, data flow, and interaction
rules; they do not need a screenshot for each implemented control.

Capture the running app with sample data and inspect the result before using it.
Normal-use examples must show loaded media, not broken previews or tiny test
images. An empty or failed state belongs only beside an explanation of that state.
Keep review-only captures with the pull request instead of adding them to the
architecture image collection. Remove unused images when their explanation goes.

The homepage and README share the sample-library image below. Keep its full-size
link, caption, and credits with it so readers can inspect the app clearly.

## Sample library screenshot

`website/images/library.jpg` shows the built application after importing ten
sample photos into an isolated library, with AI disabled. It is not a UI mockup
or a private photo collection. The same image is used in the README.

The photos come from Unsplash under the
[Unsplash License](https://unsplash.com/license), not this repository's software
license. The source images are:

- [Lake](https://images.unsplash.com/photo-1470770841072-f978cf4d019e)
- [Forest](https://images.unsplash.com/photo-1441974231531-c6227db76b6e)
- [Night sky](https://images.unsplash.com/photo-1519681393784-d120267933ba)
- [Water](https://images.unsplash.com/photo-1501785888041-af3ef285b470)
- [Waterfall](https://images.unsplash.com/photo-1433086966358-54859d0ed716)
- [Meadow](https://images.unsplash.com/photo-1500534623283-312aade485b7)
- [Woodland](https://images.unsplash.com/photo-1447752875215-b2761acb3c5d)
- [Ridge](https://images.unsplash.com/photo-1469474968028-56623f02e42e)
- [Ocean](https://images.unsplash.com/photo-1518837695005-2083093ee35b)
- [River](https://images.unsplash.com/photo-1426604966848-d7adac402bff)

The sample downloads use `w=1200&q=85&fm=jpg&cs=strip`. They have no embedded
color profiles; the current Docbank preview producer rejects ICC-tagged JPEGs.
This is a sample-data choice, not a recommendation to strip metadata from a
user's originals. Only the application screenshot is included in this repository.
