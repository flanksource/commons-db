import type { JSONPathEvalResult as JsonPathEvalResult } from "@flanksource/clicky-ui";
import { useQuery } from "@tanstack/react-query";
import { createContext, useContext, useMemo, type ReactNode } from "react";
import { fetchJSON } from "./connectionBrowserModel";

// A stable identity so a component reading "nothing sampled" does not re-render
// every parent render on a fresh [].
const EMPTY_ROWS: unknown[] = [];

// The rows the JSONPath picker browses, for the surfaces that cannot ask for
// them themselves.
//
// The entity form's extension reads the profile straight off the form root and
// samples it directly. The standalone profile editor renders its column
// inspector far from the draft it is editing, so that page samples once here and
// every column field reads the result — one request per editor rather than one
// per column, and no query client needed at the leaf.
const JsonPathSampleRowsContext = createContext<unknown[]>(EMPTY_ROWS);

export function JsonPathProfileProvider({
  profile,
  children,
}: {
  profile: unknown;
  children: ReactNode;
}) {
  const rows = useJsonPathSample(profile);
  return (
    <JsonPathSampleRowsContext.Provider value={rows}>
      {children}
    </JsonPathSampleRowsContext.Provider>
  );
}

/** Every sampled row in scope; empty where nothing has been sampled. */
export function useJsonPathSampleRows(): unknown[] {
  return useContext(JsonPathSampleRowsContext);
}


// The keys /profile/sample accepts. It decodes with DisallowUnknownFields, so
// this is a whitelist rather than a tidy-up: one stray key and the whole request
// is a 400.
//
// `columns`, `aliases` and `ignore` are left out deliberately. Those transforms
// are the thing the author is still writing — a source column gets renamed and
// consumed by them — and the picker has to offer the provider's own row shape,
// not the shape a half-written profile projects out of it.
const SAMPLE_PROFILE_KEYS = [
  "profile",
  "provider",
  "query",
  "params",
  "imports",
  "namespace",
] as const;

export function sampleRequestProfile(profile: unknown): Record<string, unknown> | null {
  if (!isRecord(profile)) return null;
  const provider = profile.provider;
  if (!isRecord(provider)) return null;
  const type = typeof provider.type === "string" ? provider.type.trim() : "";
  if (!type) return null;
  const request: Record<string, unknown> = {};
  for (const key of SAMPLE_PROFILE_KEYS) {
    if (profile[key] !== undefined) request[key] = profile[key];
  }
  const name = typeof profile.profile === "string" ? profile.profile.trim() : "";
  request.profile = name || "sample";
  return request;
}

/**
 * The rows the picker browses: a read-only sample of `profile`.
 *
 * A profile that cannot be sampled — no provider yet, or a query the backend
 * rejects — yields nothing, which leaves JSONPathField's browse button disabled
 * and the path typed by hand rather than picked.
 */
export function useJsonPathSample(profile: unknown): unknown[] {
  const request = useMemo(() => sampleRequestProfile(profile), [profile]);
  const { data } = useQuery({
    queryKey: ["jsonpath-sample", JSON.stringify(request)],
    enabled: request !== null,
    // The sample is a query against someone's backend, so it is fetched once per
    // profile shape and reused by every column's picker rather than re-run as
    // the form re-renders.
    staleTime: Infinity,
    gcTime: 5 * 60 * 1000,
    retry: false,
    refetchOnWindowFocus: false,
    queryFn: async () => {
      const result = await fetchJSON<{ rows?: Record<string, unknown>[] }>(
        "/api/v1/profile/sample",
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ profile: request, params: {} }),
        },
      );
      return result.rows ?? EMPTY_ROWS;
    },
  });
  return data ?? EMPTY_ROWS;
}

/**
 * Evaluates a JSONPath against one sampled row, server-side.
 *
 * The path has to be read by the library the query engine reads it with, or the
 * preview would confidently disagree with the column it previews. The row goes
 * up with the request because the caller already has it — re-running someone's
 * backend query on every keystroke would make the preview cost real money.
 */
export function evaluateJsonPath(request: {
  jsonpath: string;
  source?: string;
  row: unknown;
}): Promise<JsonPathEvalResult> {
  return fetchJSON<JsonPathEvalResult>("/api/v1/profile/sample/jsonpath", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(request),
  });
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
