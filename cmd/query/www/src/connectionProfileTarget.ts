import type { BrowserDescriptor } from "@flanksource/clicky-ui/profiles";

export function connectionProfileTargetOptions(
  descriptor: BrowserDescriptor | null | undefined,
  options: Record<string, unknown>,
): Record<string, unknown> | undefined {
  if (descriptor?.target?.kind === "index") {
    const value = options[descriptor.target.option];
    return typeof value === "string" && value
      ? { [descriptor.target.option]: value }
      : undefined;
  }
  return undefined;
}
