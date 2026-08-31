import { createReadStream } from "node:fs";
import { lstat } from "node:fs/promises";
import { createServer } from "node:http";
import path from "node:path";

const root = path.resolve(process.argv[2] ?? "site");
const port = Number.parseInt(process.env.PORT ?? "4173", 10);
const types = new Map([
  [".css", "text/css; charset=utf-8"],
  [".html", "text/html; charset=utf-8"],
  [".js", "text/javascript; charset=utf-8"],
  [".json", "application/json"],
  [".md", "text/markdown; charset=utf-8"],
  [".svg", "image/svg+xml"],
  [".woff2", "font/woff2"],
]);

createServer(async (request, response) => {
  try {
    const pathname = decodeURIComponent(new URL(request.url, "http://localhost").pathname);
    let target = path.resolve(root, `.${pathname}`);
    if (target !== root && !target.startsWith(`${root}${path.sep}`)) throw new Error("invalid path");
    const info = await lstat(target);
    if (info.isDirectory()) target = path.join(target, "index.html");
    response.setHeader("Content-Type", types.get(path.extname(target)) ?? "application/octet-stream");
    createReadStream(target).pipe(response);
  } catch {
    response.statusCode = 404;
    response.end("Not found\n");
  }
}).listen(port, "127.0.0.1", () => {
  process.stdout.write(`Fotobank docs: http://127.0.0.1:${port}\n`);
});
