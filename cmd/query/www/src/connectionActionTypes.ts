// Shared types and pure helpers for the connection form "Test"/"Resolve"
// actions. Kept in a non-component module so the sibling component files can
// each export exactly one component (react-doctor's only-export-components).

export type ResolvedConnection = {
  type?: string;
  namespace?: string;
  url?: string;
  username?: string;
  password?: string;
  certificate?: string;
  properties?: Record<string, string>;
};

export type TestResult = { ok: boolean; message: string; url?: string };

export type ActionOutcome =
  | { action: "test"; elapsedMs: number; result: TestResult }
  | { action: "resolve"; elapsedMs: number; resolved: ResolvedConnection }
  | { action: "test" | "resolve"; elapsedMs: number; error: string };

export async function postConnection(action: "test" | "resolve", value: unknown): Promise<unknown> {
  const res = await fetch(`/api/v1/connection/${action}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(value),
  });
  if (!res.ok) throw new Error((await res.text()) || `${res.status} ${res.statusText}`);
  return res.json();
}

export function formatDuration(elapsedMs: number): string {
  if (elapsedMs < 1) return "<1 ms";
  if (elapsedMs < 1_000) return `${Math.round(elapsedMs)} ms`;
  return `${(elapsedMs / 1_000).toFixed(elapsedMs < 10_000 ? 1 : 0)} s`;
}
