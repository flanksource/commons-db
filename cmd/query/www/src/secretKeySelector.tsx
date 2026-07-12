import { useMemo, useState } from "react";
import {
  SecretKeySelector,
  serializeSecretRef,
  parseSecretRef,
  type FieldControl,
  type KeyPreview,
  type PostExtension,
  type SecretKeyValue,
  type SecretKind,
  type SecretResource,
  type SecretValueSource,
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
// (see WorkloadUrlField) on top of the reference/literal pickers.
const SECRET_WIDGET = "k8s-secret-selector";
const URL_WIDGET = "k8s-url-selector";

// Credential fields offer every source. URL fields drop the service-account
// token (which produces a bearer token, never a URL). Values round-trip through
// the EnvVar string form the Go connection model understands (secret://,
// configmap://, helm://, serviceaccount://, op://, or a plain literal); the
// namespace is omitted and applied at runtime from the connection's namespace.
const SECRET_SOURCES: SecretValueSource[] = [
  "secret",
  "configmap",
  "helm",
  "serviceaccount",
  "onepassword",
  "value",
];
const URL_SOURCES: SecretValueSource[] = [
  "secret",
  "configmap",
  "helm",
  "onepassword",
  "value",
];

async function fetchJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) {
    throw new Error(`${url}: ${res.status} ${await res.text()}`);
  }
  return (await res.json()) as T;
}

// SecretField renders the SecretKeySelector with loaders scoped to the
// connection's selected namespace (so the operator only sees resources in that
// namespace). For the url field it adds a Reference/Workload mode toggle.
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
  const loadServiceAccounts = useMemo(
    () => (): Promise<SecretResource[]> =>
      fetchJSON<SecretResource[]>(`/api/v1/secrets?kind=serviceaccount${nsQuery}`),
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

  const current = parseSecretRef(field.value);
  // Host/username/url fields seed the inline "Value" source (x-clicky-default-source);
  // secrets default to the Secret picker (component's first source).
  const defaultSource = field.schema["x-clicky-default-source"];
  const seeded: SecretKeyValue | undefined =
    current ?? (defaultSource === "value" ? { kind: "value", value: "" } : undefined);
  const selector = (
    <SecretKeySelector
      value={seeded}
      onChange={(next) => field.onChange(serializeSecretRef(next))}
      loadResources={loadResources}
      loadKeyPreview={loadKeyPreview}
      loadServiceAccounts={loadServiceAccounts}
      sources={allowWorkload ? URL_SOURCES : SECRET_SOURCES}
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
