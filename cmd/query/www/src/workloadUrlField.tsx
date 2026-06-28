import { useMemo, useState } from "react";
import {
  Combobox,
  WorkloadPicker,
  parseWorkloadKey,
  type ComboboxOption,
  type WorkloadKind,
  type WorkloadResource,
} from "@flanksource/clicky-ui";
import { SegmentedControl } from "./segmentedControl";

// The URL field's Workload source. The operator picks a Service (svc/ip/proxy/
// portforward) or an Ingress (host), and the selection is stored as a
// `<strategy>://<name>.<namespace>[:<port>]` URL that the Go side expands at
// hydration time against the cluster (see context.expandServiceURL). The
// application scheme (postgres, http, ...) is derived from the connection type at
// resolution (host:// upgrades to https when the ingress terminates TLS), so only
// the strategy + workload coordinates are stored here.

export type WorkloadStrategy = "svc" | "ip" | "proxy" | "host" | "portforward";

const STRATEGY_OPTIONS: { value: WorkloadStrategy; label: string; title: string }[] = [
  { value: "svc", label: "Service", title: "In-cluster Service DNS (svc://)" },
  { value: "ip", label: "Cluster IP", title: "Service ClusterIP (ip://)" },
  { value: "proxy", label: "Proxy", title: "API-server service proxy (proxy://)" },
  { value: "host", label: "Ingress", title: "Ingress host (host://)" },
  { value: "portforward", label: "Port-forward", title: "Local SPDY port-forward tunnel, any backend (portforward://)" },
];

const STRATEGIES = new Set<WorkloadStrategy>(["svc", "ip", "proxy", "host", "portforward"]);

type ParsedWorkloadUrl = {
  strategy: WorkloadStrategy;
  name: string;
  namespace: string;
  port: string;
};

// isWorkloadUrl reports whether a stored URL value is a workload reference (vs a
// literal/secret value handled by the SecretKeySelector).
export function isWorkloadUrl(raw: unknown): boolean {
  return typeof raw === "string" && /^(svc|ip|proxy|host|portforward):\/\//.test(raw);
}

// parseWorkloadUrl splits `<strategy>://<name>[.<namespace>][:<port>]` into parts.
export function parseWorkloadUrl(raw: unknown): ParsedWorkloadUrl | undefined {
  if (typeof raw !== "string") return undefined;
  const m = /^([a-z]+):\/\/([^/:]+)(?::(\d+))?/.exec(raw);
  if (!m || !STRATEGIES.has(m[1] as WorkloadStrategy)) return undefined;
  const strategy = m[1] as WorkloadStrategy;
  const host = m[2];
  const dot = host.indexOf(".");
  const name = dot >= 0 ? host.slice(0, dot) : host;
  const namespace = dot >= 0 ? host.slice(dot + 1) : "";
  return { strategy, name, namespace, port: m[3] ?? "" };
}

function serializeWorkloadUrl(p: ParsedWorkloadUrl): string {
  if (!p.name) return "";
  const host = p.namespace ? `${p.name}.${p.namespace}` : p.name;
  // host:// (ingress) carries no port — the ingress fronts the standard 80/443.
  return p.port && p.strategy !== "host" ? `${p.strategy}://${host}:${p.port}` : `${p.strategy}://${host}`;
}

async function fetchJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${url}: ${res.status} ${await res.text()}`);
  return (await res.json()) as T;
}

export function WorkloadUrlField({
  value,
  namespace,
  onChange,
}: {
  value: unknown;
  namespace: string;
  onChange: (next: string) => void;
}) {
  const current = parseWorkloadUrl(value);
  const name = current?.name ?? "";
  const port = current?.port ?? "";

  // Strategy lives in local state so it survives the empty value that exists
  // before a workload is picked (the serialized URL can't encode a strategy with
  // no name). Seeded from the stored value when editing.
  const [strategy, setStrategy] = useState<WorkloadStrategy>(current?.strategy ?? "svc");

  // host strategy picks Ingresses (resolved by name); the rest pick Services.
  const isHost = strategy === "host";
  const kind: WorkloadKind = isHost ? "ingress" : "service";

  // One fetch of the namespace's workloads of the active kind, shared by the
  // picker (selection) and the port dropdown (the chosen service's ports).
  const [resources, setResources] = useState<WorkloadResource[]>([]);
  const loadWorkloads = useMemo(() => {
    const promise = namespace
      ? fetchJSON<Record<WorkloadKind, WorkloadResource[]>>(
          `/api/v1/workloads?namespace=${encodeURIComponent(namespace)}&kinds=${kind}`,
        )
      : Promise.resolve({} as Record<WorkloadKind, WorkloadResource[]>);
    promise.then((res) => setResources(res[kind] ?? [])).catch(() => setResources([]));
    return () => promise;
  }, [namespace, kind]);

  const emit = (next: Partial<ParsedWorkloadUrl>) =>
    onChange(serializeWorkloadUrl({ strategy, name, namespace, port, ...next }));

  // Switching in/out of host changes the resource kind (service ↔ ingress), so
  // the selected name no longer applies — reset it (and the port).
  const onStrategy = (next: WorkloadStrategy) => {
    setStrategy(next);
    if ((next === "host") !== isHost) onChange("");
    else emit({ strategy: next });
  };

  const selected = resources.find((r) => r.name === name);
  const portOptions = useMemo<ComboboxOption[]>(
    () =>
      (selected?.ports ?? []).map((p) => ({
        value: String(p.number),
        label: p.name ? `${p.name} (${p.number})` : String(p.number),
      })),
    [selected],
  );

  const ingressOptions = useMemo<ComboboxOption[]>(
    () =>
      resources.map((r) => ({
        value: r.name,
        label: r.hosts?.length ? `${r.hosts[0]} (${r.name})` : r.name,
      })),
    [resources],
  );

  const pickerValue = name ? `${namespace ? `${namespace}/` : ""}service/${name}` : "";

  return (
    <div className="space-y-2">
      <SegmentedControl
        value={strategy}
        options={STRATEGY_OPTIONS}
        onChange={onStrategy}
        ariaLabel="Resolution strategy"
      />
      {isHost ? (
        <Combobox
          options={ingressOptions}
          value={name}
          onChange={(n) => emit({ name: n })}
          placeholder="Select ingress…"
          ariaLabel="Ingress"
        />
      ) : (
        <>
          <WorkloadPicker
            value={pickerValue}
            onChange={(key) => emit({ name: key ? parseWorkloadKey(key).name : "", port: "" })}
            loadWorkloads={loadWorkloads}
            namespace={namespace}
            kinds={["service"]}
          />
          <Combobox
            options={portOptions}
            value={port}
            onChange={(p) => emit({ port: p })}
            allowCustomValue
            placeholder="Port"
            ariaLabel="Port"
          />
        </>
      )}
    </div>
  );
}
