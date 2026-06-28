import { useMemo, useState } from "react";
import {
  SecretKeySelector,
  type FieldControl,
  type KeyPreview,
  type PostExtension,
  type SecretKeyValue,
  type SecretKind,
  type SecretResource,
} from "@flanksource/clicky-ui";
import { WorkloadUrlField, isWorkloadUrl } from "./workloadUrlField";
import { SegmentedControl } from "./segmentedControl";

type UrlMode = "reference" | "workload";

const URL_MODE_OPTIONS: { value: UrlMode; label: string }[] = [
  { value: "reference", label: "Reference" },
  { value: "workload", label: "Workload" },
];

// The connection schema tags credential fields (password/certificate/username)
// with k8s-secret-selector so the form renders a SecretKeySelector instead of a
// plain text input. URL fields use k8s-url-selector, which adds a Workload source
// (see WorkloadUrlField) on top of the secret/literal pickers.
const SECRET_WIDGET = "k8s-secret-selector";
const URL_WIDGET = "k8s-url-selector";

// Values round-trip through the EnvVar string form the Go connection model
// already understands: `secret://<name>/<key>`, `configmap://<name>/<key>`, or a
// plain literal. The namespace is intentionally omitted — it is applied at
// runtime from the connection's namespace when the reference is resolved.
function parseRef(raw: unknown): SecretKeyValue | undefined {
  if (typeof raw !== "string" || raw === "") return undefined;
  for (const kind of ["secret", "configmap"] as const) {
    const prefix = `${kind}://`;
    if (raw.startsWith(prefix)) {
      const [name = "", key = ""] = raw.slice(prefix.length).split("/", 2);
      return { kind, name, key };
    }
  }
  return { kind: "value", value: raw };
}

function serializeRef(value: SecretKeyValue | undefined): string {
  if (!value) return "";
  if (value.kind === "value") return value.value;
  return `${value.kind}://${value.name}/${value.key}`;
}

async function fetchJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) {
    throw new Error(`${url}: ${res.status} ${await res.text()}`);
  }
  return (await res.json()) as T;
}

// SecretField renders the SecretKeySelector with loaders scoped to the
// connection's selected namespace (so the operator only sees Secrets/ConfigMaps
// in that namespace). For the url field it adds a Reference/Workload mode toggle.
function SecretField({
  field,
  namespace,
  allowWorkload,
}: {
  field: FieldControl;
  namespace: string;
  allowWorkload: boolean;
}) {
  const [workloadMode, setWorkloadMode] = useState(() => allowWorkload && isWorkloadUrl(field.value));

  // Loaders are memoised by namespace so their identity is stable across renders
  // (SecretKeySelector refetches when it changes) but tracks the namespace.
  const nsQuery = namespace ? `&namespace=${encodeURIComponent(namespace)}` : "";
  const loadResources = useMemo(
    () => (kind: SecretKind): Promise<SecretResource[]> =>
      fetchJSON<SecretResource[]>(`/api/v1/secrets?kind=${kind}${nsQuery}`),
    [nsQuery],
  );
  const loadKeyPreview = useMemo(
    () => (kind: SecretKind, name: string): Promise<KeyPreview[]> =>
      fetchJSON<KeyPreview[]>(
        `/api/v1/secrets/preview?kind=${kind}&name=${encodeURIComponent(name)}${nsQuery}`,
      ),
    [nsQuery],
  );

  const modeToggle = (
    <SegmentedControl
      value={workloadMode ? "workload" : "reference"}
      options={URL_MODE_OPTIONS}
      onChange={(m) => setWorkloadMode(m === "workload")}
      ariaLabel="URL source"
    />
  );

  if (allowWorkload && workloadMode) {
    return (
      <div className="space-y-2">
        {modeToggle}
        <WorkloadUrlField value={field.value} namespace={namespace} onChange={field.onChange} />
      </div>
    );
  }

  const current = parseRef(field.value);
  // Host/username fields seed the inline "Value" toggle (x-clicky-default-source);
  // secrets default to the Secret picker.
  const defaultSource = field.schema["x-clicky-default-source"];
  const seeded: SecretKeyValue | undefined =
    current ?? (defaultSource === "value" ? { kind: "value", value: "" } : undefined);
  const selector = (
    <SecretKeySelector
      value={seeded}
      onChange={(next) => field.onChange(serializeRef(next))}
      loadResources={loadResources}
      loadKeyPreview={loadKeyPreview}
    />
  );

  if (!allowWorkload) return selector;
  return (
    <div className="space-y-2">
      {modeToggle}
      {selector}
    </div>
  );
}

// secretKeySelectorPost replaces the default text input of any field tagged
// `x-clicky-component: k8s-secret-selector` (credentials) or `k8s-url-selector`
// (URLs, which add a Workload source) with the SecretKeySelector widget, keeping
// the form's label. It reads the form's selected namespace from ctx.rootValue to
// scope the cluster lookups.
const secretKeySelectorPost: PostExtension = (field, nodes, ctx) => {
  const component = field.schema["x-clicky-component"];
  if (component !== SECRET_WIDGET && component !== URL_WIDGET) return nodes;
  const namespace = (ctx?.rootValue?.namespace as string) ?? "";
  return {
    label: nodes.label,
    value: <SecretField field={field} namespace={namespace} allowWorkload={component === URL_WIDGET} />,
  };
};

export const secretFormExtensions = { post: [secretKeySelectorPost] };
