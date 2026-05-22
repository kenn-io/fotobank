import createClient, { type ClientOptions } from "openapi-fetch";
import type { paths } from "./generated/schema";

export type Client = ReturnType<typeof createClient<paths>>;

// createApiClient builds a typed openapi-fetch client. F1 callers pass
// nothing and get a same-origin client; tests inject a custom fetch
// to mock the API.
export function createApiClient(
  baseUrl: string = "",
  options: Pick<ClientOptions, "fetch" | "querySerializer"> = {},
): Client {
  return createClient<paths>({ baseUrl, ...options });
}

// Default singleton used at app boot. Empty baseUrl = same-origin.
export const api: Client = createApiClient("");
