// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Thin typed client over the generated contract types. Every path, query,
// path parameter and body type comes from src/schema.d.ts, which is
// generated from core/api/openapi.yaml and never edited by hand; when the
// contract changes, the drift check regenerates the types and the compiler
// walks every call site. The runtime is the platform fetch: no dependency,
// no interceptor chain, no cache. The desk wires this in R1-8.

import type { paths } from "./schema.js";

export type { components, operations, paths } from "./schema.js";

/** One HTTP method's operation object as the generated types carry it. */
type Operation = Record<string, unknown>;

/** The query parameters an operation declares, if any. */
type QueryOf<Op extends Operation> = Op extends { parameters: { query?: infer Q } } ? Q : Record<string, never>;

/** The path parameters an operation declares, if any. */
type PathOf<Op extends Operation> = Op extends { parameters: { path: infer P } } ? P : Record<string, never>;

/** The JSON request body an operation accepts, if any. */
type BodyOf<Op extends Operation> = Op extends { requestBody: { content: { "application/json": infer B } } } ? B : never;

/** The JSON body a 2xx response carries; undefined for the bare 204s. */
type SuccessOf<Op extends Operation> =
  Op extends { responses: { 200: { content: { "application/json": infer R } } } } ? R
    : Op extends { responses: { 201: { content: { "application/json": infer R } } } } ? R
      : undefined;

/** Paths that carry the given method. */
type PathsOf<M extends string> = {
  [P in keyof paths & string]: paths[P] extends { [K in M]: Operation } ? P : never;
}[keyof paths & string];

/** Per-request options: typed query and path parameters, plus headers. */
interface RequestOptions<Op extends Operation> {
  query?: QueryOf<Op>;
  path?: PathOf<Op>;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  idempotencyKey?: string;
}

/** A finished call: status, typed body, and the request id for support. */
export interface ApiResponse<T> {
  status: number;
  ok: boolean;
  body: T;
  requestId: string | null;
}

/** A non 2xx response. The body is whatever error envelope arrived. */
export class ApiError extends Error {
  readonly status: number;
  readonly body: unknown;
  readonly requestId: string | null;

  constructor(status: number, body: unknown, requestId: string | null) {
    super(`API request failed with status ${status}${requestId ? ` (request ${requestId})` : ""}`);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
    this.requestId = requestId;
  }
}

export interface ClientOptions {
  /** The API origin, such as "https://erp.example.com" (no trailing slash). */
  baseUrl: string;
  /** Defaults to the platform fetch. */
  fetch?: typeof globalThis.fetch;
  /** Headers sent with every request, such as Authorization. */
  headers?: Record<string, string>;
}

export interface Client {
  get<P extends PathsOf<"get">>(path: P, options?: RequestOptions<paths[P]["get"]>): Promise<ApiResponse<SuccessOf<paths[P]["get"]>>>;
  post<P extends PathsOf<"post">>(path: P, body: BodyOf<paths[P]["post"]> extends never ? undefined : BodyOf<paths[P]["post"]>, options?: RequestOptions<paths[P]["post"]>): Promise<ApiResponse<SuccessOf<paths[P]["post"]>>>;
  put<P extends PathsOf<"put">>(path: P, body: BodyOf<paths[P]["put"]> extends never ? undefined : BodyOf<paths[P]["put"]>, options?: RequestOptions<paths[P]["put"]>): Promise<ApiResponse<SuccessOf<paths[P]["put"]>>>;
  patch<P extends PathsOf<"patch">>(path: P, body: BodyOf<paths[P]["patch"]> extends never ? undefined : BodyOf<paths[P]["patch"]>, options?: RequestOptions<paths[P]["patch"]>): Promise<ApiResponse<SuccessOf<paths[P]["patch"]>>>;
  delete<P extends PathsOf<"delete">>(path: P, options?: RequestOptions<paths[P]["delete"]>): Promise<ApiResponse<SuccessOf<paths[P]["delete"]>>>;
}

export function createClient(options: ClientOptions): Client {
  const doFetch = options.fetch ?? globalThis.fetch.bind(globalThis);
  const base = options.baseUrl.replace(/\/+$/, "");

  async function request(
    method: string,
    templated: string,
    body: unknown,
    requestOptions: RequestOptions<Operation> | undefined,
  ): Promise<ApiResponse<unknown>> {
    const path = substitute(templated, requestOptions?.path);
    const url = base + path + querystring(requestOptions?.query);
    const headers: Record<string, string> = { ...options.headers, ...requestOptions?.headers };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (requestOptions?.idempotencyKey) headers["Idempotency-Key"] = requestOptions.idempotencyKey;

    const response = await doFetch(url, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: requestOptions?.signal,
    });
    const requestId = response.headers.get("X-Request-ID");
    const text = await response.text();
    const parsed = text === "" ? undefined : safeJson(text);
    if (!response.ok) throw new ApiError(response.status, parsed, requestId);
    return { status: response.status, ok: true, body: parsed, requestId };
  }

  return {
    get: (path, opts) => request("GET", path, undefined, opts as RequestOptions<Operation> | undefined) as never,
    post: (path, body, opts) => request("POST", path, body, opts as RequestOptions<Operation> | undefined) as never,
    put: (path, body, opts) => request("PUT", path, body, opts as RequestOptions<Operation> | undefined) as never,
    patch: (path, body, opts) => request("PATCH", path, body, opts as RequestOptions<Operation> | undefined) as never,
    delete: (path, opts) => request("DELETE", path, undefined, opts as RequestOptions<Operation> | undefined) as never,
  };
}

/** Fills {param} segments with the typed path parameters, encoded. */
function substitute(templated: string, params: Record<string, string> | undefined): string {
  if (!params) return templated;
  return templated.replace(/\{([^}/]+)\}/g, (whole, name: string) => {
    const value = params[name];
    if (value === undefined) throw new Error(`missing path parameter ${name} for ${templated}`);
    return encodeURIComponent(value);
  });
}

/** Serializes the query object; undefined and null values are skipped. */
function querystring(query: Record<string, unknown> | undefined): string {
  if (!query) return "";
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null) continue;
    if (Array.isArray(value)) {
      for (const item of value) search.append(key, String(item));
    } else {
      search.append(key, String(value));
    }
  }
  const encoded = search.toString();
  return encoded === "" ? "" : `?${encoded}`;
}

function safeJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}
