/**
 * Loads a list parameter's values from a file the user picks.
 *
 * The browser hands over a File and never a path, so the CLI's `@ids.csv#region`
 * selector has no analogue here: the column is chosen from the headers the file
 * turns out to have, offered once the file is read. Everything else — the
 * formats, the trimming, the dedupe, the `!` exclusions — follows the same rules
 * as `--param ids=@ids.csv`, because both go through the same parser contract.
 */

import { Button, Select } from "@flanksource/clicky-ui";
import { useRef, useState } from "react";
import {
  parseListValueFile,
  summarizeListValueLoad,
  type ListValueParse,
} from "./listValueFile";
import {
  mergeIntoMultiFilter,
  type MergeResult,
  type MultiFilterMode,
  type MultiFilterValue,
} from "./listValueMerge";

const ACCEPTED = ".csv,.json,.txt";

/**
 * ListValueFileButton reads a file and hands back its values. It keeps the file
 * so a different column can be chosen without picking it again — the column only
 * becomes a question once the headers are known.
 */
export function ListValueFileButton({
  title,
  onValues,
}: {
  title: string;
  onValues: (values: string[], parsed: ListValueParse) => number;
}) {
  const input = useRef<HTMLInputElement>(null);
  const [summary, setSummary] = useState("");
  const [file, setFile] = useState<{ name: string; text: string } | null>(null);
  const [columns, setColumns] = useState<string[]>([]);

  const load = (name: string, text: string, selector?: string) => {
    const parsed = parseListValueFile(name, text, selector);
    setColumns(parsed.columns);
    setSummary(summarizeListValueLoad(parsed, parsed.error ? 0 : onValues(parsed.values, parsed)));
  };

  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2">
        <Button onClick={() => input.current?.click()} title={title}>
          Load from file…
        </Button>
        <input
          ref={input}
          type="file"
          accept={ACCEPTED}
          className="hidden"
          onChange={async (event) => {
            const picked = event.target.files?.[0];
            if (!picked) return;
            const text = await picked.text();
            setFile({ name: picked.name, text });
            load(picked.name, text);
            // Clear the input so picking the same file again re-reads it.
            event.target.value = "";
          }}
        />
        {columns.length > 1 && file ? (
          <Select
            aria-label="Column"
            onChange={(event) => load(file.name, file.text, event.target.value)}
            options={columns.map((column) => ({ value: column, label: column }))}
          />
        ) : null}
      </div>
      {summary ? <span className="text-xs text-muted-foreground">{summary}</span> : null}
    </div>
  );
}

/**
 * ListValuePicker loads a file into a tri-state selection, for the run-time
 * filter bar. The mode applies to the whole file, except for values carrying
 * their own `!`, which are excluded either way.
 */
export function ListValuePicker({
  label,
  value,
  onChange,
}: {
  label: string;
  value: MultiFilterValue;
  onChange: (next: MultiFilterValue) => void;
}) {
  const [mode, setMode] = useState<MultiFilterMode>("include");
  const selected = Object.keys(value).length;

  const apply = (values: string[]): number => {
    const merge: MergeResult = mergeIntoMultiFilter(value, values, mode, "replace");
    onChange(merge.next);
    return merge.added + merge.flipped;
  };

  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2">
        <span className="text-xs text-muted-foreground">{label}</span>
        <span className="text-xs text-muted-foreground">
          {selected > 0 ? `${selected} selected` : "None selected"}
        </span>
        <Select
          aria-label="Load as"
          value={mode}
          onChange={(event) => setMode(event.target.value as MultiFilterMode)}
          options={[
            { value: "include", label: "Include" },
            { value: "exclude", label: "Exclude" },
          ]}
        />
      </div>
      <ListValueFileButton
        title={`Load ${label} values from a CSV, JSON or text file`}
        onValues={apply}
      />
    </div>
  );
}
