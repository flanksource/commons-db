import { useEffect, useState } from "react";
import { HoverCard, SplitButton, type FormActionsRenderer } from "@flanksource/clicky-ui";
import { UiCheckFilled, UiWarningCircleFilled } from "@flanksource/clicky-ui/icons";

// connectionFormActions adds a "Test" split-button (with a "Resolve values"
// dropdown option) to the connection create/edit form footer. Test probes the
// resolved URL's reachability; Resolve hydrates the draft (expanding secret:// and
// svc:// / ip:// / proxy:// / host:// workload URLs) and shows the resolved values
// with secrets masked. Both POST the live form value to the backend, so they work
// before the connection is saved. Other entities get no extra actions.
export const connectionFormActions: FormActionsRenderer = ({ value, action }) =>
  action.path.includes("/connection") ? <ConnectionTestButton value={value} /> : null;

type ResolvedConnection = {
  type?: string;
  namespace?: string;
  url?: string;
  username?: string;
  password?: string;
  certificate?: string;
  properties?: Record<string, string>;
};

type TestResult = { ok: boolean; message: string; url?: string };

type ActionOutcome =
  | { action: "test"; elapsedMs: number; result: TestResult }
  | { action: "resolve"; elapsedMs: number; resolved: ResolvedConnection }
  | { action: "test" | "resolve"; elapsedMs: number; error: string };

async function postConnection(action: "test" | "resolve", value: unknown): Promise<unknown> {
  const res = await fetch(`/api/v1/connection/${action}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(value),
  });
  if (!res.ok) throw new Error((await res.text()) || `${res.status} ${res.statusText}`);
  return res.json();
}

function ConnectionTestButton({ value }: { value: Record<string, unknown> }) {
  const [pending, setPending] = useState<"test" | "resolve" | null>(null);
  const [outcome, setOutcome] = useState<ActionOutcome | null>(null);

  // A result only describes the exact draft that was tested. Remove it as soon
  // as any form field changes so a stale success cannot follow edited values.
  useEffect(() => setOutcome(null), [value]);

  const run = async (action: "test" | "resolve") => {
    setPending(action);
    setOutcome(null);
    const started = performance.now();
    try {
      const data = await postConnection(action, value);
      const elapsedMs = performance.now() - started;
      setOutcome(
        action === "test"
          ? { action, elapsedMs, result: data as TestResult }
          : { action, elapsedMs, resolved: data as ResolvedConnection },
      );
    } catch (err) {
      setOutcome({
        action,
        elapsedMs: performance.now() - started,
        error: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setPending(null);
    }
  };

  return (
    <div className="flex items-center gap-3" aria-live="polite">
      {outcome && <ConnectionActionStatus outcome={outcome} />}
      <SplitButton
        label={pending === "test" ? "Testing…" : pending === "resolve" ? "Resolving…" : "Test"}
        variant="outline"
        loading={pending !== null}
        disabled={pending !== null}
        onClick={() => run("test")}
        items={[{ label: "Resolve values", onSelect: () => run("resolve") }]}
        title="Connection actions"
      />
    </div>
  );
}

function ConnectionActionStatus({ outcome }: { outcome: ActionOutcome }) {
  const failed = "error" in outcome || (outcome.action === "test" && !outcome.result.ok);
  const label = failed ? "Failed" : outcome.action === "test" ? "Reachable" : "Resolved";
  const detail =
    "error" in outcome ? (
      <ErrorView message={outcome.error} />
    ) : outcome.action === "test" ? (
      <TestView result={outcome.result} />
    ) : (
      <ResolveView resolved={outcome.resolved} />
    );
  const trigger = (
    <button
      type="button"
      className={
        failed
          ? "inline-flex items-center gap-1.5 text-sm font-medium text-destructive"
          : "inline-flex items-center gap-1.5 text-sm font-medium text-emerald-600"
      }
      aria-label={`${label} in ${formatDuration(outcome.elapsedMs)}; show details`}
    >
      {failed ? (
        <UiWarningCircleFilled size={16} className="text-destructive" />
      ) : (
        <UiCheckFilled size={16} className="text-emerald-600" />
      )}
      <span>{label}</span>
      <span className="font-normal text-muted-foreground">· {formatDuration(outcome.elapsedMs)}</span>
    </button>
  );

  return (
    <HoverCard
      trigger={trigger}
      placement="top"
      delay={120}
      cardClassName="w-96 whitespace-normal p-3 text-sm"
    >
      {detail}
    </HoverCard>
  );
}

function formatDuration(elapsedMs: number): string {
  if (elapsedMs < 1) return "<1 ms";
  if (elapsedMs < 1_000) return `${Math.round(elapsedMs)} ms`;
  return `${(elapsedMs / 1_000).toFixed(elapsedMs < 10_000 ? 1 : 0)} s`;
}

function TestView({ result }: { result: TestResult }) {
  return (
    <div className="space-y-1">
      <div className="font-medium">Connection test</div>
      <div className="whitespace-pre-wrap break-words text-muted-foreground">{result.message}</div>
      {result.url && <div className="break-all font-mono text-xs text-muted-foreground">{result.url}</div>}
    </div>
  );
}

function ResolveView({ resolved }: { resolved: ResolvedConnection }) {
  const rows: [string, string][] = [];
  const add = (label: string, v?: string) => {
    if (v) rows.push([label, v]);
  };
  add("URL", resolved.url);
  add("Username", resolved.username);
  add("Password", resolved.password);
  add("Certificate", resolved.certificate);
  for (const [k, v] of Object.entries(resolved.properties ?? {})) add(k, v);

  if (rows.length === 0) return <div className="text-muted-foreground">Nothing to resolve.</div>;
  return (
    <div className="space-y-2">
      <div className="font-medium">Resolved values</div>
      <dl className="space-y-1">
        {rows.map(([label, v]) => (
          <div key={label} className="grid grid-cols-[7rem_1fr] gap-2">
            <dt className="text-muted-foreground">{label}</dt>
            <dd className="break-all font-mono text-xs">{v}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

function ErrorView({ message }: { message: string }) {
  return <div className="whitespace-pre-wrap break-words text-destructive">{message}</div>;
}
