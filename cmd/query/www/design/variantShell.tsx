import { mockFields, previewColumns, previewRows } from "./mockProfile";

/**
 * The /profile-os2 page rendered behind an overlay variant, so the drawer and
 * modal options can be judged on how much context they preserve.
 */
export function BackdropTable({ onClose }: { onClose: () => void }) {
  const labelFor = (name: string) =>
    mockFields.find((field) => field.name === name)?.label ?? name;
  return (
    <button
      type="button"
      aria-label="Close"
      className="min-w-0 flex-1 cursor-default overflow-hidden bg-background text-left"
      onClick={onClose}
    >
      <div className="border-b border-border px-4 py-2.5">
        <p className="text-sm font-semibold">os2</p>
        <p className="text-[11px] text-muted-foreground">1,284 rows · last 15 minutes</p>
      </div>
      <table className="w-full border-collapse text-left">
        <thead>
          <tr>
            {previewColumns.map((name) => (
              <th key={name} className="whitespace-nowrap border-b border-border px-3 py-1.5 text-xs font-medium text-muted-foreground">
                {labelFor(name)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {[...previewRows, ...previewRows, ...previewRows].map((row, index) => (
            <tr key={index}>
              {previewColumns.map((name) => (
                <td key={name} className="whitespace-nowrap border-b border-border px-3 py-1.5 font-mono text-xs">
                  {String(row[name as keyof typeof row])}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="pointer-events-none absolute inset-0 bg-black/20" />
    </button>
  );
}
