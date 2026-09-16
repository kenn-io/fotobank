import { makeRouteSafe } from "@orval/core";
import { defineConfig } from "orval";

export default defineConfig({
  fotobank: {
    input: {
      target: "../openapi.yaml",
      filters: { mode: "exclude", tags: ["streams"] },
    },
    output: {
      target: "src/lib/api/generated/client.ts",
      schemas: "src/lib/api/generated/models",
      client: (clients) => {
        const generator = clients["axios-functions"];
        return {
          ...generator,
          client: (operation, options) =>
            generator.client(operation, { ...options, route: makeRouteSafe(options.route) }),
        };
      },
      override: {
        operations: {
          "add-album-media": { operationName: () => "addMediaToAlbum" },
        },
        mutator: { path: "src/lib/api/transport.ts", name: "request" },
      },
    },
  },
  browser: {
    input: {
      target: "../openapi.yaml",
      filters: { mode: "include", tags: ["streams"] },
    },
    output: {
      target: "src/lib/api/generated/browser.ts",
      client: "fetch",
      urlEncodeParameters: true,
      override: { fetch: { includeHttpResponseReturnType: false } },
    },
  },
});
