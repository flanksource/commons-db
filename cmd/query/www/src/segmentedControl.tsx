// SegmentedControl is a small inline radio-style toggle used by the connection
// form's custom URL widget (the Reference/Workload mode switch and the workload
// resolution-strategy selector), matching the SecretKeySelector's segmented
// Secret/ConfigMap/Value buttons.
export function SegmentedControl<T extends string>({
  value,
  options,
  onChange,
  ariaLabel,
}: {
  value: T;
  options: { value: T; label: string; title?: string }[];
  onChange: (value: T) => void;
  ariaLabel?: string;
}) {
  return (
    <div role="radiogroup" aria-label={ariaLabel} className="inline-flex gap-0.5 rounded-md border border-border p-0.5">
      {options.map((o) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={active}
            title={o.title ?? o.label}
            onClick={() => onChange(o.value)}
            className={`rounded px-2 py-0.5 text-xs font-medium ${
              active ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-muted"
            }`}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}
