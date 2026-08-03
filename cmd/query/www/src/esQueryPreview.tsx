/**
 * The DSL a specification compiles to. The server compiles it — the same code
 * path a query runs through — so the preview is the query, not a re-derivation
 * of it that could drift.
 */

import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { fetchJSON } from "./connectionBrowserModel";
import type { EsSearch } from "./esQueryBuilderModel";

export type EsCompileResult = { query: string; size: number; from: number };

export type EsCompilation = {
  query: string;
  size?: number;
  from?: number;
  /** The compiler's own message for a specification it rejected. */
  error?: string;
  loading: boolean;
};

/** compileRequestBody is what POST /compile takes, with the empty parts left off. */
export function compileRequestBody(input: {
  search: EsSearch;
  params?: Record<string, unknown>;
  roles?: Record<string, string>;
}): string {
  const { search, params, roles } = input;
  return JSON.stringify({
    search,
    ...(params && Object.keys(params).length ? { params } : {}),
    ...(roles && Object.keys(roles).length ? { roles } : {}),
  });
}

export function errorMessage(error: unknown): string | undefined {
  if (!error) return undefined;
  return error instanceof Error ? error.message : String(error);
}

/**
 * useCompiledSearch compiles the specification as it is edited. The body is
 * debounced rather than the request, so an edit that lands back on a body
 * already compiled costs nothing.
 */
export function useCompiledSearch(input: {
  baseUrl: string;
  search: EsSearch;
  params?: Record<string, unknown>;
  roles?: Record<string, string>;
  enabled?: boolean;
  debounceMs?: number;
}): EsCompilation {
  const { baseUrl, enabled = true, debounceMs = 250 } = input;
  const body = compileRequestBody(input);
  const [settled, setSettled] = useState(body);
  useEffect(() => {
    const timer = setTimeout(() => setSettled(body), debounceMs);
    return () => clearTimeout(timer);
  }, [body, debounceMs]);

  const compiled = useQuery({
    queryKey: ["es-compile", baseUrl, settled],
    queryFn: () =>
      fetchJSON<EsCompileResult>(`${baseUrl}/compile`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: settled,
      }),
    enabled: enabled && baseUrl !== "",
    retry: 0,
  });

  return {
    query: compiled.data?.query ?? "",
    ...(compiled.data ? { size: compiled.data.size, from: compiled.data.from } : {}),
    ...(compiled.error ? { error: errorMessage(compiled.error) } : {}),
    loading: compiled.isFetching,
  };
}

export function EsQueryPreview({
  compilation,
  className,
}: {
  compilation: EsCompilation;
  className?: string;
}) {
  const { query, size, from, error, loading } = compilation;
  return (
    <section className={className}>
      <header className="flex items-center gap-2">
        <h3 className="text-xs font-medium text-muted-foreground">Compiled DSL</h3>
        {size === undefined ? null : (
          <span className="text-xs text-muted-foreground">
            size {size}
            {from ? ` · from ${from}` : ""}
          </span>
        )}
        {loading ? (
          <span className="text-xs text-muted-foreground">compiling…</span>
        ) : null}
      </header>
      {error ? (
        <p
          role="alert"
          className="mt-1 whitespace-pre-wrap rounded-md border border-destructive/40 bg-destructive/5 p-2 font-mono text-xs text-destructive"
        >
          {error}
        </p>
      ) : (
        <pre className="mt-1 max-h-64 overflow-auto rounded-md border bg-muted/40 p-2 font-mono text-xs">
          {query}
        </pre>
      )}
    </section>
  );
}
