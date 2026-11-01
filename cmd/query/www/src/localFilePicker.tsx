import { useState } from "react";
import { Button, LocalPathPicker, isLocalHostname, type FieldControl, type LocalPathLoader, type PostExtension } from "@flanksource/clicky-ui";
import { sqlitePath, sqliteFileURL } from "./sqliteFileURL";
import { mountPath } from "./mount";

const load: LocalPathLoader = async (path, signal) => {
  const response = await fetch(`${mountPath}/api/v1/connections/files?path=${encodeURIComponent(path)}`, { signal });
  if (!response.ok) throw new Error(await response.text());
  return response.json();
};

function BrowseFile({ field }: { field: FieldControl }) {
  const [open, setOpen] = useState(false);
  const value = typeof field.value === "string" ? field.value : "";
  return <><Button type="button" variant="outline" onClick={() => setOpen(true)}>Browse…</Button>
    {open && <LocalPathPicker open={open} onClose={() => setOpen(false)} initialPath={sqlitePath(value)} accept={[".db", ".sqlite", ".sqlite3"]} load={load} onSelect={path => field.onChange(sqliteFileURL(path,value))} />}
  </>;
}

const localFileExtension: PostExtension = (field, nodes, context) => {
  if (typeof window === "undefined" || !isLocalHostname(window.location.hostname) || field.readOnly || field.schema["x-local-file"] !== "sqlite") return nodes;
  const root = context?.rootValue;
  const provider = root?.provider as { type?: string; options?: { type?: string } } | undefined;
  if (provider?.type === "sql" && provider.options?.type !== "sqlite") return nodes;
  const value = typeof field.value === "string" ? field.value : "";
  if (value.includes("://") && !value.startsWith("sqlite://") && !value.startsWith("file:")) return nodes;
  return { ...nodes, value: <div className="space-y-2">{nodes.value}<BrowseFile field={field} /></div> };
};

export const localFileFormExtensions = { post: [localFileExtension] };
