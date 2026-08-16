import {
  ConnectionLoggingPolicy,
  isConnectionLoggingCapability,
  type FieldControl,
  type PostExtension,
  type PostExtensionContext,
  type PreExtension,
} from "@flanksource/clicky-ui";

const WIDGET = "connection-logging-policy";

function capability(field: FieldControl) {
  const value = field.schema["x-clicky-logging"];
  if (!isConnectionLoggingCapability(value)) {
    throw new Error(
      "connection logging schema is missing valid x-clicky-logging metadata",
    );
  }
  return value;
}

function ConnectionLoggingField({
  field,
  ctx,
}: {
  field: FieldControl;
  ctx: PostExtensionContext;
}) {
  if (!ctx.rootValue || !ctx.onRootChange) {
    throw new Error(
      "connection logging requires root form value and onRootChange",
    );
  }
  const rootValue = ctx.rootValue;
  const onRootChange = ctx.onRootChange;
  const properties = isRecord(rootValue.properties)
    ? stringProperties(rootValue.properties)
    : {};

  return (
    <ConnectionLoggingPolicy
      definition={capability(field)}
      value={properties}
      readOnly={field.readOnly ?? false}
      onChange={(next) => onRootChange({ ...rootValue, properties: next })}
    />
  );
}

const connectionLoggingPre: PreExtension = (field) => {
  if (field.schema["x-clicky-component"] !== WIDGET) return field;
  capability(field);
  return {
    ...field,
    kind: "display",
    displayVariant: "spacer",
    colSpan: "full",
  };
};

const connectionLoggingPost: PostExtension = (field, nodes, ctx) => {
  if (field.schema["x-clicky-component"] !== WIDGET) return nodes;
  if (!ctx) throw new Error("connection logging form context is required");
  return {
    label: null,
    value: <ConnectionLoggingField field={field} ctx={ctx} />,
  };
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function stringProperties(
  value: Record<string, unknown>,
): Record<string, string> {
  const properties: Record<string, string> = {};
  for (const [key, item] of Object.entries(value)) {
    if (typeof item === "string") properties[key] = item;
  }
  return properties;
}

export const connectionLoggingFormExtensions = {
  pre: [connectionLoggingPre],
  post: [connectionLoggingPost],
};
