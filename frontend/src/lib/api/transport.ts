import type { ErrorModel } from "./generated/models";

export interface RequestOptions extends RequestInit {
  baseUrl?: string;
  fetch?: typeof fetch;
}

export interface APIResult<T> {
  data?: T;
  error?: ErrorModel;
  response: Response;
}

interface RequestConfig {
  url: string;
  method: string;
  headers?: Record<string, string>;
  params?: object | undefined;
  responseType?: "blob";
  data?: unknown;
}

export async function request<T>(
  config: RequestConfig,
  options: RequestOptions = {},
): Promise<APIResult<T>> {
  const { baseUrl = "", fetch: fetchFn = globalThis.fetch, ...init } = options;
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(config.params ?? {})) {
    if (value == null) continue;
    for (const item of Array.isArray(value) ? value : [value]) {
      query.append(key, String(item));
    }
  }
  const suffix = query.size ? `?${query}` : "";
  const headers = new Headers(config.headers);
  new Headers(init.headers).forEach((value, key) => headers.set(key, value));
  const response = await fetchFn(`${baseUrl}${config.url}${suffix}`, {
    ...init,
    method: config.method,
    headers,
    ...(config.data === undefined ? {} : { body: JSON.stringify(config.data) }),
  });
  if (response.ok && config.responseType === "blob") {
    return { data: (await response.blob()) as T, response };
  }
  const text = await response.text();
  let body: unknown;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = { detail: text };
    }
  }
  return response.ok
    ? { data: body as T, response }
    : { error: (body ?? {}) as ErrorModel, response };
}
