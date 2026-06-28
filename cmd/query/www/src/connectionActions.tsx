import { useState, type ReactNode } from "react";
import { SplitButton, type FormActionsRenderer } from "@flanksource/clicky-ui";

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
  const [panel, setPanel] = useState<ReactNode>(null);

  const run = async (action: "test" | "resolve") => {
    setPending(action);
    setPanel(null);
    try {
      const data = await postConnection(action, value);
      setPanel(action === "test" ? <TestView result={data as TestResult} /> : <ResolveView resolved={data as ResolvedConnection} />);
    } catch (err) {
      setPanel(<ErrorView message={err instanceof Error ? err.message : String(err)} />);
    } finally {
      setPending(null);
    }
  };

  return (
    <div className="relative">
      <SplitButton
        label="Test"
        variant="outline"
        loading={pending !== null}
        onClick={() => run("test")}
        items={[{ label: "Resolve values", onSelect: () => run("resolve") }]}
        title="Connection actions"
      />
      {panel && (
        <div className="absolute bottom-full right-0 z-50 mb-2 w-96 rounded-md border border-border bg-popover p-3 text-sm text-popover-foreground shadow-md">
          <div className="mb-2 flex items-center justify-between">
            <span className="font-medium">Result</span>
            <button type="button" className="text-muted-foreground hover:text-foreground" onClick={() => setPanel(null)}>
              ✕
            </button>
          </div>
          {panel}
        </div>
      )}
    </div>
  );
}

function TestView({ result }: { result: TestResult }) {
  return (
    <div className="space-y-1">
      <div className={result.ok ? "font-medium text-emerald-600" : "font-medium text-destructive"}>
        {result.ok ? "✓ Reachable" : "✗ Failed"}
      </div>
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
    <dl className="space-y-1">
      {rows.map(([label, v]) => (
        <div key={label} className="grid grid-cols-[7rem_1fr] gap-2">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="break-all font-mono text-xs">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function ErrorView({ message }: { message: string }) {
  return <div className="whitespace-pre-wrap break-words text-destructive">{message}</div>;
}
