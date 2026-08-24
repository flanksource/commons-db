import {
  JSONPathField,
  type FieldControl,
  type PostExtension,
  type PostExtensionContext,
} from "@flanksource/clicky-ui";
import {
  evaluateJsonPath,
  useJsonPathSample,
} from "@flanksource/clicky-ui/profiles";

// The column schema tags the `jsonpath` field with this hint so the form renders
// a browsable tree of a sample row instead of a bare text input.
const WIDGET = "jsonpath-picker";

// The picker restarts its paths at `$` inside a column holding JSON as text, so
// such a path only resolves once that column is named as the column's `source`.
// The two are one edit: the extension writes its own field and then its sibling
// through the form root, rather than committing half a column and telling the
// author to finish it.
function JsonPathFieldControl({
  field,
  ctx,
}: {
  field: FieldControl;
  ctx?: PostExtensionContext;
}) {
  const rows = useJsonPathSample(ctx?.rootValue);
  const source = pickedSource(ctx);
  return (
    <JSONPathField
      aria-label="JSONPath"
      value={(field.value as string) ?? ""}
      onChange={(next) => field.onChange(next)}
      onSelectPath={(next, { root }) => {
        field.onChange(next);
        writeSiblingSource(ctx, root);
      }}
      {...(source ? { source } : {})}
      {...(rows.length === 0 ? {} : { json: rows[0], rows })}
      evaluate={evaluateJsonPath}
      {...(field.readOnly === undefined ? {} : { disabled: field.readOnly })}
    />
  );
}

/**
 * The `source` already declared beside this field's `jsonpath`.
 *
 * The same sibling the picker writes, read back: a column that pairs the two
 * has its path written against that column decoded, so browsing or evaluating
 * from the row would report no matches for a column that works.
 */
export function pickedSource(ctx: PostExtensionContext | undefined): string | undefined {
  const { instancePath, rootValue } = ctx ?? {};
  if (!instancePath || !rootValue) return undefined;
  const segments = decodePointer(instancePath);
  if (segments.length === 0) return undefined;
  const column = readAtPath(rootValue, segments.slice(0, -1));
  const source = isRecord(column) ? column.source : undefined;
  return typeof source === "string" && source !== "" ? source : undefined;
}

function readAtPath(target: unknown, path: string[]): unknown {
  return path.reduce<unknown>((value, segment) => {
    if (Array.isArray(value)) return value[Number(segment)];
    return isRecord(value) ? value[segment] : undefined;
  }, target);
}

function writeSiblingSource(ctx: PostExtensionContext | undefined, root: string | undefined) {
  const { instancePath, rootValue, onRootChange } = ctx ?? {};
  if (!instancePath || !rootValue || !onRootChange) return;
  onRootChange(applyPickedSource(rootValue, instancePath, root));
}

/**
 * Records the column a picked path is rooted at, as `source` on the column whose
 * `jsonpath` field `instancePath` points at.
 *
 * A path picked outside any encoded column clears the source rather than leaving
 * the previous one behind: beside a jsonpath, `source` is the root, and a stale
 * root re-roots the new path at a column it was never written against — which
 * resolves to nothing and reads as a bad path.
 */
export function applyPickedSource(
  rootValue: Record<string, unknown>,
  instancePath: string,
  root: string | undefined,
): Record<string, unknown> {
  const segments = decodePointer(instancePath);
  if (segments.length === 0) return rootValue;
  return setAtPath(rootValue, [...segments.slice(0, -1), "source"], root) as Record<
    string,
    unknown
  >;
}

/** Reads an RFC 6901 pointer into its unescaped segments. */
function decodePointer(pointer: string): string[] {
  return pointer
    .split("/")
    .slice(1)
    .map((segment) => segment.split("~1").join("/").split("~0").join("~"));
}

/**
 * Returns a copy of `target` with `path` set to `value`, or with the leaf
 * removed when the value is undefined. Copies only the containers along the
 * path — the form compares by identity, so rewriting untouched branches would
 * re-render every other field.
 */
function setAtPath(target: unknown, path: string[], value: unknown): unknown {
  const [head, ...rest] = path;
  if (head === undefined) return value;
  if (Array.isArray(target)) {
    const index = Number(head);
    if (!Number.isInteger(index) || index < 0 || index >= target.length) return target;
    const copy = [...target];
    copy[index] = setAtPath(copy[index], rest, value);
    return copy;
  }
  const record = isRecord(target) ? target : {};
  if (rest.length === 0 && value === undefined) {
    if (!(head in record)) return target;
    const { [head]: _dropped, ...remaining } = record;
    return remaining;
  }
  return { ...record, [head]: setAtPath(record[head], rest, value) };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

const jsonPathPickerPost: PostExtension = (field, nodes, ctx) => {
  if (field.schema["x-clicky-component"] !== WIDGET) return nodes;
  return {
    label: nodes.label,
    // The form root is the profile document being edited, which is exactly what
    // the sample endpoint takes.
    value: <JsonPathFieldControl field={field} {...(ctx ? { ctx } : {})} />,
  };
};

export const jsonPathFormExtensions = { post: [jsonPathPickerPost] };
