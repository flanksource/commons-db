import { HoverCard } from "@flanksource/clicky-ui";
import { UiCheckFilled, UiWarningCircleFilled } from "@flanksource/clicky-ui/icons";
import type { ActionOutcome } from "./connectionActionTypes";
import { formatDuration } from "./connectionActionTypes";
import { ErrorView } from "./connectionErrorView";
import { TestView } from "./connectionTestView";
import { ResolveView } from "./connectionResolveView";

export function ConnectionActionStatus({ outcome }: { outcome: ActionOutcome }) {
  const failed = "error" in outcome || (outcome.action === "test" && !outcome.result.ok);
  const label = failed ? "Failed" : outcome.action === "test" ? "Reachable" : "Resolved";
  const detail =
    "error" in outcome ? (
      <ErrorView message={outcome.error} />
    ) : outcome.action === "test" ? (
      <TestView result={outcome.result} />
    ) : (
      <ResolveView resolved={outcome.resolved} />
    );
  const trigger = (
    <button
      type="button"
      className={
        failed
          ? "inline-flex items-center gap-1.5 text-sm font-medium text-destructive"
          : "inline-flex items-center gap-1.5 text-sm font-medium text-emerald-600"
      }
      aria-label={`${label} in ${formatDuration(outcome.elapsedMs)}; show details`}
    >
      {failed ? (
        <UiWarningCircleFilled size={16} className="text-destructive" />
      ) : (
        <UiCheckFilled size={16} className="text-emerald-600" />
      )}
      <span>{label}</span>
      <span className="font-normal text-muted-foreground">· {formatDuration(outcome.elapsedMs)}</span>
    </button>
  );

  return (
    <HoverCard
      trigger={trigger}
      placement="top"
      delay={120}
      cardClassName="w-96 whitespace-normal p-3 text-sm"
    >
      {detail}
    </HoverCard>
  );
}
