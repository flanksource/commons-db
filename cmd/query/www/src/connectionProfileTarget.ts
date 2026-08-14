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
  return undefined;
}
