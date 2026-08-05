/**
 * Which operators a field accepts. The operator vocabulary itself comes from
 * the schema (`x-es-operators`, emitted from Go's esdsl.Catalog), so the only
 * knowledge owned here is how a mapping type reduces to a family and how the
 * resulting operators are ordered for an author.
 */

import {
  defaultOperatorForFamily,
  type EsCondition,
} from "./esQueryBuilderModel";

/** One entry of x-es-operators, keyed exactly as esdsl.OperatorInfo marshals. */
export type EsOperatorInfo = {
  op: string;
  label: string;
  arity: "none" | "single" | "multiple" | "range" | "group";
  needsField?: boolean;
  acceptsFields?: boolean;
  fieldTypes: string[];
  analyzed?: boolean;
  group?: boolean;
};

export function changeConditionOperator(
  condition: EsCondition,
  next: EsOperatorInfo,
): EsCondition {
  const { value, values, gt, gte, lt, lte, conditions, ...rest } = condition;
  const changed: EsCondition = { ...rest, op: next.op };

  if (next.arity === "single") {
    const operand = value !== undefined
      ? value
      : values?.length === 1
        ? values[0]
        : undefined;
    return operand === undefined ? changed : { ...changed, value: operand };
  }
  if (next.arity === "multiple") {
    const operands = values?.length
      ? values
      : value === undefined
        ? []
        : [value];
    return operands.length === 0 ? changed : { ...changed, values: operands };
  }
  if (next.arity === "range") {
    return {
      ...changed,
      ...(gt !== undefined ? { gt } : {}),
      ...(gte !== undefined ? { gte } : {}),
      ...(lt !== undefined ? { lt } : {}),
      ...(lte !== undefined ? { lte } : {}),
    };
  }
  if (next.arity === "group") {
    return { ...changed, conditions: conditions ?? [] };
  }
  return changed;
}

/** A field as _field_caps reports it, via the browser inspection response. */
export type EsFieldMapping = {
  name: string;
  dataType?: string;
  types?: string[];
  searchable?: boolean;
  aggregatable?: boolean;
  conflicting?: boolean;
};

/** An offered operator. `advanced` marks one that fights the field's analysis. */
export type OfferedOperator = EsOperatorInfo & { advanced?: boolean };

const families: Record<string, string> = {
  keyword: "keyword",
  constant_keyword: "keyword",
  wildcard: "keyword",
  text: "text",
  match_only_text: "text",
  search_as_you_type: "text",
  date: "date",
  date_nanos: "date",
  long: "number",
  integer: "number",
  short: "number",
  byte: "number",
  double: "number",
  float: "number",
  half_float: "number",
  scaled_float: "number",
  unsigned_long: "number",
  boolean: "boolean",
  ip: "ip",
  ip_range: "ip",
  nested: "nested",
  object: "object",
  flattened: "object",
};

// Families whose values are run through an analyzer at index time, so an exact
// operator on them matches tokens rather than the stored text.
const analyzedFamilies = new Set(["text"]);

// Structural families hold other fields rather than a value: they are descended
// into or tested for presence, never matched against.
const structuralFamilies = new Set(["nested", "object"]);

export function fieldFamily(field: EsFieldMapping | undefined): string {
  const mapped = field?.dataType || field?.types?.[0] || "";
  return families[mapped] ?? "any";
}

/**
 * operatorsForField ranks the applicable operators: the family's default first,
 * then the rest that suit it, then the type-independent ones, and finally the
 * ones that fight the field's analysis — those carry `advanced`.
 */
export function operatorsForField(
  catalog: EsOperatorInfo[],
  field: EsFieldMapping | undefined,
): OfferedOperator[] {
  if (!field) return catalog.map((entry) => ({ ...entry }));
  if (field.searchable === false) {
    return catalog.filter((entry) => entry.op === "exists").map((entry) => ({ ...entry }));
  }
  const family = fieldFamily(field);
  if (structuralFamilies.has(family)) {
    return catalog
      .filter((entry) => entry.op === "exists" || entry.fieldTypes.includes(family))
      .sort((left, right) => Number(left.op === "exists") - Number(right.op === "exists"))
      .map((entry) => ({ ...entry }));
  }

  const analyzed = analyzedFamilies.has(family);
  const preferred = defaultOperatorForFamily(family);
  const ranked = catalog.flatMap((entry, position) => {
    if (entry.group) return [];
    const direct = entry.fieldTypes.includes(family);
    const generic = entry.fieldTypes.includes("any");
    if (!direct && !generic) return [];
    if (direct && entry.op === preferred) return [{ entry, rank: 0, position }];
    if (direct && Boolean(entry.analyzed) !== analyzed) {
      return [{ entry: { ...entry, advanced: true }, rank: 3, position }];
    }
    return [{ entry, rank: direct ? 1 : 2, position }];
  });
  return ranked
    .sort((left, right) => left.rank - right.rank || left.position - right.position)
    .map(({ entry }) => ({ ...entry }));
}

/**
 * fieldWarning explains a field the author cannot query the way they expect. A
 * conflicting field is still offered — hiding it would silently drop a field
 * that works on most of the matched indexes.
 */
export function fieldWarning(field: EsFieldMapping): string | undefined {
  if (field.conflicting) {
    const types = field.types ?? (field.dataType ? [field.dataType] : []);
    return `${field.name} is mapped as ${joinWithAnd(types)} across indexes`;
  }
  if (field.searchable === false) {
    return `${field.name} is not searchable, so only exists applies`;
  }
  return undefined;
}

/** sortableFields lists what can order the hits: doc values, _score and _doc. */
export function sortableFields(fields: EsFieldMapping[]): string[] {
  return [
    ...fields.filter((field) => field.aggregatable !== false).map((field) => field.name),
    "_score",
    "_doc",
  ];
}

/**
 * qualifiersForOperator narrows the advanced settings to the ones the operator
 * emits. `restrictions` is the schema's x-es-qualifiers table, which lists only
 * the qualifiers the compiler restricts — a name it omits applies everywhere.
 */
export function qualifiersForOperator(options: {
  names: string[];
  restrictions?: Record<string, string[]>;
  op: string;
}): string[] {
  const { names, restrictions = {}, op } = options;
  return names.filter((name) => !restrictions[name] || restrictions[name].includes(op));
}

type OptionsSchema = { properties?: Record<string, unknown> } | undefined;

type SchemaNode = {
  properties?: Record<string, SchemaNode>;
  items?: SchemaNode;
  enum?: string[];
  "x-es-operators"?: EsOperatorInfo[];
  "x-es-occurs"?: string[];
  "x-es-qualifiers"?: Record<string, string[]>;
};

function searchProperty(schema: OptionsSchema): SchemaNode | undefined {
  return schema?.properties?.search as SchemaNode | undefined;
}

/** operatorCatalogFromSchema reads x-es-operators off the search property. */
export function operatorCatalogFromSchema(schema: OptionsSchema): EsOperatorInfo[] {
  return searchProperty(schema)?.["x-es-operators"] ?? [];
}

/** esQualifiersFromSchema is the same read for the qualifier-to-operator table. */
export function esQualifiersFromSchema(
  schema: OptionsSchema,
): Record<string, string[]> | undefined {
  return searchProperty(schema)?.["x-es-qualifiers"];
}

/** One advanced qualifier, as the condition schema describes it. */
export type EsQualifierSchema = {
  title?: string;
  description?: string;
  type?: string;
  enum?: string[];
  default?: unknown;
};

/**
 * Everything about a search that Go owns: the operator catalog, the bool
 * clauses, the advanced qualifiers and the sort orders. The builder reads all of
 * it off the schema, so adding a qualifier or an operator in esdsl reaches the
 * editor without a frontend change.
 */
export type EsBuilderVocabulary = {
  catalog: EsOperatorInfo[];
  occurs: string[];
  qualifierNames: string[];
  qualifierRestrictions?: Record<string, string[]>;
  qualifiers: Record<string, EsQualifierSchema>;
  sortOrders: string[];
};

// The condition properties that carry structure rather than an advanced
// setting. Whatever the schema holds beyond these is a qualifier.
const structuralConditionKeys = new Set([
  "op",
  "occur",
  "field",
  "fields",
  "value",
  "values",
  "gt",
  "gte",
  "lt",
  "lte",
  "optional",
  "when",
  "conditions",
]);

export function esBuilderVocabulary(schema: OptionsSchema): EsBuilderVocabulary {
  const search = searchProperty(schema);
  const conditionProperties = search?.properties?.query?.properties ?? {};
  const qualifiers: Record<string, EsQualifierSchema> = {};
  for (const [name, property] of Object.entries(conditionProperties)) {
    if (structuralConditionKeys.has(name)) continue;
    qualifiers[name] = property as EsQualifierSchema;
  }
  const restrictions = esQualifiersFromSchema(schema);
  return {
    catalog: operatorCatalogFromSchema(schema),
    occurs: search?.["x-es-occurs"] ?? [],
    qualifierNames: Object.keys(qualifiers),
    ...(restrictions ? { qualifierRestrictions: restrictions } : {}),
    qualifiers,
    sortOrders: search?.properties?.sort?.items?.properties?.order?.enum ?? [],
  };
}

function joinWithAnd(values: string[]): string {
  if (values.length < 2) return values.join("");
  return `${values.slice(0, -1).join(", ")} and ${values[values.length - 1]}`;
}
