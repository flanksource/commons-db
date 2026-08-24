import type { TestResult } from "./connectionActionTypes";

export function TestView({ result }: { result: TestResult }) {
  return (
    <div className="space-y-1">
      <div className="font-medium">Connection test</div>
      <div className="whitespace-pre-wrap break-words text-muted-foreground">{result.message}</div>
      {result.url && <div className="break-all font-mono text-xs text-muted-foreground">{result.url}</div>}
    </div>
  );
}
