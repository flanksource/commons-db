import { NamespacePicker, type FieldControl, type PostExtension } from "@flanksource/clicky-ui";

// The connection schema tags the `namespace` field with this hint so the form
// renders a NamespacePicker. The chosen namespace scopes the secret and workload
// lookups of the other fields (they read it from the form's root value).
const WIDGET = "k8s-namespace-selector";

async function fetchJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${url}: ${res.status} ${await res.text()}`);
  return (await res.json()) as T;
}

// Module-level so its identity is stable across renders (NamespacePicker loads
// once on mount).
const loadNamespaces = (): Promise<string[]> => fetchJSON<string[]>("/api/v1/namespaces");

function NamespaceField({ field }: { field: FieldControl }) {
  return (
    <NamespacePicker
      value={(field.value as string) ?? ""}
      onChange={(next) => field.onChange(next)}
      loadNamespaces={loadNamespaces}
    />
  );
}

const namespacePickerPost: PostExtension = (field, nodes) => {
  if (field.schema["x-clicky-component"] !== WIDGET) return nodes;
  return { label: nodes.label, value: <NamespaceField field={field} /> };
};

export const namespaceFormExtensions = { post: [namespacePickerPost] };
