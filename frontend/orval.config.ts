import { makeRouteSafe } from "@orval/core";
import { defineConfig } from "orval";

export default defineConfig({
  fotobank: {
    input: "../openapi.yaml",
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
});
