import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { prepareOutput } from "./build.mjs";

test("rebuild preserves unmarked output and replaces interrupted owned output", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "fotobank-docs-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const output = path.join(root, "site");
  await mkdir(output);
  await writeFile(path.join(output, "index.html"), "keep this previous site");

  await prepareOutput(output);
  const cache = path.join(root, ".cache");
  const saved = await readdir(cache);
  assert.equal(saved.length, 1);
  const previous = path.join(cache, saved[0], "site", "index.html");
  assert.equal(await readFile(previous, "utf8"), "keep this previous site");

  // A build interrupted after preparation must be replaceable without
  // preserving another copy or needing manual cleanup.
  await writeFile(path.join(output, "partial.html"), "unfinished build");
  await prepareOutput(output);
  await assert.rejects(readFile(path.join(output, "partial.html")), { code: "ENOENT" });
  assert.deepEqual(await readdir(cache), saved);
  assert.equal(await readFile(previous, "utf8"), "keep this previous site");
});
