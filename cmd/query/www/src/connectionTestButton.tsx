import { useState } from "react";
import { SplitButton } from "@flanksource/clicky-ui";
import type { ActionOutcome, ResolvedConnection, TestResult } from "./connectionActionTypes";
import { postConnection } from "./connectionActionTypes";
import { ConnectionActionStatus } from "./connectionActionStatus";

export function ConnectionTestButton({
  value,
  canSubmit,
}: {
  value: Record<string, unknown>;
  canSubmit: boolean;
}) {
  const [pending, setPending] = useState<"test" | "resolve" | null>(null);
  const [result, setResult] = useState<{ outcome: ActionOutcome; forValue: unknown } | null>(null);

  // A result only describes the exact draft that was tested. Rather than
  // tracking the prop in an effect to clear stale state, tie the result to
  // the value it was computed for and derive visibility during render: once
  // the form value changes, `forValue` no longer matches `value` and the
  // stale outcome is dropped without a render showing it.
  const outcome = result && result.forValue === value ? result.outcome : null;

  const run = async (action: "test" | "resolve") => {
    const forValue = value;
    setPending(action);
    setResult(null);
    const started = performance.now();
    try {
      const data = await postConnection(action, value);
      const elapsedMs = performance.now() - started;
      setResult({
        forValue,
        outcome:
          action === "test"
            ? { action, elapsedMs, result: data as TestResult }
            : { action, elapsedMs, resolved: data as ResolvedConnection },
      });
    } catch (err) {
      setResult({
        forValue,
        outcome: {
          action,
          elapsedMs: performance.now() - started,
          error: err instanceof Error ? err.message : String(err),
        },
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
        disabled={pending !== null || !canSubmit}
        onClick={() => run("test")}
        items={[{ label: "Resolve values", onSelect: () => run("resolve") }]}
        title="Connection actions"
      />
    </div>
  );
}
