import {
  JSONPathField,
  type FieldControl,
  type PostExtension,
} from "@flanksource/clicky-ui";
import { evaluateJsonPath, useJsonPathSample } from "./jsonPathSampleRow";

// The column schema tags the `jsonpath` field with this hint so the form renders
// a browsable tree of a sample row instead of a bare text input.
const WIDGET = "jsonpath-picker";

// The picker is rooted at the whole row, so the paths it writes are row-rooted
// and stand on their own. A column's `source` narrows the root the backend
// evaluates against, but an extension cannot read its own row's sibling fields,
// so pairing the two is left to whoever writes `source` by hand — which is why
// no onSelectPath is passed here: the playground names the column a decoded path
// needs as its Source, and the author sets it in the field beside this one.
function JsonPathFieldControl({
  field,
  profile,
}: {
  field: FieldControl;
  profile: unknown;
}) {
  const rows = useJsonPathSample(profile);
  return (
    <JSONPathField
      aria-label="JSONPath"
      value={(field.value as string) ?? ""}
      onChange={(next) => field.onChange(next)}
      {...(rows.length === 0 ? {} : { json: rows[0], rows })}
      evaluate={evaluateJsonPath}
      {...(field.readOnly === undefined ? {} : { disabled: field.readOnly })}
    />
  );
}

const jsonPathPickerPost: PostExtension = (field, nodes, ctx) => {
  if (field.schema["x-clicky-component"] !== WIDGET) return nodes;
  return {
    label: nodes.label,
    // The form root is the profile document being edited, which is exactly what
    // the sample endpoint takes.
    value: <JsonPathFieldControl field={field} profile={ctx?.rootValue} />,
  };
};

export const jsonPathFormExtensions = { post: [jsonPathPickerPost] };
