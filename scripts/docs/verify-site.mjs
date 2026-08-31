import { lstat, readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

const requiredPaths = [
  "index.html",
  "index.md",
  "guide/index.html",
  "guide.md",
  "docs/index.html",
  "docs/architecture/index.html",
  "styles/site.css",
  "favicon.svg",
  "llms.txt",
];

async function exists(target) {
  try {
    await lstat(target);
    return true;
  } catch (error) {
    if (error?.code === "ENOENT") return false;
    throw error;
  }
}

async function htmlFiles(root) {
  const files = [];
  async function walk(directory) {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const target = path.join(directory, entry.name);
      if (entry.isDirectory()) await walk(target);
      else if (entry.isFile() && entry.name.endsWith(".html")) files.push(target);
    }
  }
  await walk(root);
  return files;
}

function localTarget(site, file, href) {
  const origin = "https://fotobank.kenn.io";
  const page = new URL(`/${path.relative(site, file).split(path.sep).join("/")}`, origin);
  const parsed = new URL(href, page);
  if (parsed.origin !== origin) return undefined;
  let target = path.join(site, decodeURIComponent(parsed.pathname));
  if (parsed.pathname.endsWith("/")) target = path.join(target, "index.html");
  return target;
}

export async function verifySite(site) {
  for (const relative of requiredPaths) {
    if (!(await exists(path.join(site, relative)))) {
      throw new Error(`generated site is missing ${relative}`);
    }
  }

  for (const file of await htmlFiles(site)) {
    const html = await readFile(file, "utf8");
    for (const match of html.matchAll(/(?:href|src)="([^"]+)"/g)) {
      const href = match[1];
      if (href.startsWith("#") || href.startsWith("mailto:") || href.startsWith("data:")) continue;
      const target = localTarget(site, file, href);
      if (target && !(await exists(target))) {
        throw new Error(`${path.relative(site, file)} links to missing ${href}`);
      }
    }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const site = path.resolve(process.argv[2] ?? "site");
  await verifySite(site);
  process.stdout.write(`verified documentation site at ${site}\n`);
}
