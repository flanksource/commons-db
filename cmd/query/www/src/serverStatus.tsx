import type { ConnectionInfo } from "./connectionInfoTypes";

export function ServerStatus({ server }: { server: ConnectionInfo["server"] }) {
  const tone =
    server.status === "available"
      ? "text-emerald-600 dark:text-emerald-400"
      : server.status === "error"
        ? "text-destructive"
        : "text-muted-foreground";
  const label =
    server.status === "available"
      ? "available"
      : server.status === "error"
        ? "unreachable"
        : "unavailable";
  return (
    <span
      className={`inline-flex shrink-0 items-center gap-1 ${tone}`}
      title={server.message ?? undefined}
    >
      <span className="inline-block h-1.5 w-1.5 rounded-full bg-current" />
      {label}
    </span>
  );
}
