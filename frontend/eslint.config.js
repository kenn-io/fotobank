import js from "@eslint/js";
import { defineConfig, globalIgnores } from "eslint/config";
import prettier from "eslint-config-prettier";
import svelte from "eslint-plugin-svelte";
import globals from "globals";
import ts from "typescript-eslint";

export default defineConfig(
  globalIgnores([
    "dist/**",
    "playwright-report/**",
    "test-results/**",
    "src/lib/api/generated/**",
  ]),
  js.configs.recommended,
  ts.configs.recommended,
  svelte.configs.recommended,
  prettier,
  {
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
    },
    rules: {
      "@typescript-eslint/no-unused-vars": [
        "error",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
          caughtErrorsIgnorePattern: "^_",
        },
      ],
      // Non-reactive Maps and URLSearchParams also serve as local calculations
      // and imperative Leaflet bookkeeping; they need not become Svelte state.
      "svelte/prefer-svelte-reactivity": "off",
      // These are refactoring preferences, not checks for incorrect behavior.
      "svelte/prefer-writable-derived": "off",
      "svelte/no-useless-children-snippet": "off",
      // Plain text lists have no child state that needs keyed identity.
      "svelte/require-each-key": "off",
    },
  },
  {
    files: ["**/*.svelte", "**/*.svelte.ts", "**/*.svelte.js"],
    languageOptions: {
      parserOptions: { parser: ts.parser },
    },
  },
  {
    files: ["**/*.test.ts"],
    rules: {
      // API stubs intentionally accept partial payloads and malformed responses.
      "@typescript-eslint/no-explicit-any": "off",
      // Leaflet spies capture the actual instance from prototype method calls.
      "@typescript-eslint/no-this-alias": "off",
    },
  },
);
