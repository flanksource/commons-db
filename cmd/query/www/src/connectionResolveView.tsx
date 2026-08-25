import type { ResolvedConnection } from "./connectionActionTypes";

export function ResolveView({ resolved }: { resolved: ResolvedConnection }) {
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
