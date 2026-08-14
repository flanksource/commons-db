import type { BrowserDescriptor } from "@flanksource/clicky-ui/profiles";

export function connectionProfileTargetOptions(
  descriptor: BrowserDescriptor | null | undefined,
  options: Record<string, unknown>,
): Record<string, unknown> | undefined {
  if (descriptor?.target?.kind === "index") {
    return typeof options.index === "string" && options.index
      ? { index: options.index }
      : undefined;
  }
  if (descriptor?.target?.kind !== "kubernetes-workload") return undefined;

  const kind = typeof options.kind === "string" ? options.kind : "";
  const namespace =
    typeof options.namespace === "string" ? options.namespace : "";
  const name = typeof options.name === "string" ? options.name : "";
  return kind && namespace && name ? { kind, namespace, name } : undefined;
}
