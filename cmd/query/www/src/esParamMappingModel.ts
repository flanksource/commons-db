import type {
  ConditionPath,
  EsCondition,
  EsSearch,
  EsValue,
} from "./esQueryBuilderModel";
import {
  conditionAt,
  emptyGroup,
  isParamValue,
  updateAt,
} from "./esQueryBuilderModel";
import { conditionOperandPatch } from "./esQueryOperandModel";
import type { ParamDraft } from "./profileWizardModel";

export type ParamOperand = "value" | "values" | "gt" | "gte" | "lt" | "lte";

export type ParamMapping = {
  path: ConditionPath;
  field: string;
  operand: ParamOperand;
};

export type ParamMappingEdit = {
  search: EsSearch;
  params: ParamDraft[];
};

export function paramMappings(
  search: EsSearch | undefined,
  name: string,
): ParamMapping[] {
  const found: ParamMapping[] = [];
  const walk = (condition: EsCondition | undefined, path: ConditionPath) => {
    if (!condition) return;
    for (const operand of operands) {
      const value = condition[operand];
      const values = Array.isArray(value) ? value : [value];
      if (
        condition.field &&
        values.some((entry) => isParamValue(entry) && entry.param === name)
      ) {
        found.push({ path, field: condition.field, operand });
      }
    }
    condition.conditions?.forEach((child, index) =>
      walk(child, [...path, index]),
    );
  };
  walk(search?.query, []);
  return found;
}

export function addParamMapping({
  search,
  params,
  name,
  field,
}: ParamMappingEdit & { name: string; field: string }): ParamMappingEdit {
  const param = namedParam(params, name);
  if (param.role === "limit" || param.role === "offset") {
    throw new Error(
      `${param.role} parameter ${name} cannot map to a query field`,
    );
  }
  if (param.role === "time-from" || param.role === "time-to") {
    return { search: { ...search, timeField: field }, params };
  }
  const withoutPrevious =
    param.type === "list" ? stripParamReferences(search, name) : search;
  const condition: EsCondition = {
    op: param.type === "list" ? "terms" : "term",
    occur: "filter",
    field,
    value: { param: name },
    ...(!param.required ? { optional: true } : {}),
  };
  return {
    search: appendCondition(withoutPrevious, condition),
    params: syncNativeField(
      params,
      name,
      param.type === "list" ? field : undefined,
    ),
  };
}

export function bindParamOperand({
  search,
  params,
  path,
  operand,
  name,
}: ParamMappingEdit & {
  path: ConditionPath;
  operand: ParamOperand;
  name: string;
}): ParamMappingEdit {
  const param = namedParam(params, name);
  if (param.role && param.role !== "filter") {
    throw new Error(
      `${param.role} parameter ${name} cannot bind a query operand`,
    );
  }
  const condition = search.query && conditionAt(search.query, path);
  if (!condition?.field)
    throw new Error(`condition for parameter ${name} has no field`);
  const value = { param: name };
  const patch =
    operand === "values"
      ? conditionOperandPatch({ arity: "multiple", values: [value] })
      : operand === "value"
        ? conditionOperandPatch({ arity: "single", value })
        : conditionOperandPatch({ arity: "range", bound: operand, value });
  const query = updateAt(search.query as EsCondition, path, (current) => ({
    ...current,
    ...patch,
  }));
  const bound = { ...search, query };
  return {
    search:
      param.type === "list" ? stripParamReferences(bound, name, path) : bound,
    params: syncNativeField(
      params,
      name,
      param.type === "list" ? condition.field : undefined,
    ),
  };
}

export function removeParamMapping({
  search,
  params,
  name,
  path,
}: ParamMappingEdit & {
  name: string;
  path?: ConditionPath;
}): ParamMappingEdit {
  const param = namedParam(params, name);
  if (param.role === "time-from" || param.role === "time-to") {
    const next = { ...search };
    delete next.timeField;
    return { search: next, params };
  }
  if (!path) {
    if (param.type !== "list")
      throw new Error(`parameter ${name} has no field mapping to remove`);
    return { search, params: syncNativeField(params, name, undefined) };
  }
  const query = search.query
    ? editAt(search.query, path, (condition) =>
        removeReference(condition, name),
      )
    : undefined;
  return {
    search: { ...search, query: query ?? emptyGroup() },
    params: syncNativeField(params, name, undefined),
  };
}

export function reconcileParamMappings({
  search,
  previous,
  next,
}: {
  search: EsSearch;
  previous: ParamDraft[];
  next: ParamDraft[];
}): ParamMappingEdit {
  let reconciled = search;
  const previousNames = new Set(
    previous.map((param) => param.name).filter(Boolean),
  );
  const nextNames = new Set(next.map((param) => param.name).filter(Boolean));
  const renamed = new Set<string>();
  if (previous.length === next.length) {
    previous.forEach((param, index) => {
      const oldName = param.name;
      const newName = next[index]?.name;
      if (
        oldName &&
        newName &&
        oldName !== newName &&
        !nextNames.has(oldName) &&
        !previousNames.has(newName)
      ) {
        reconciled = renameParamReferences(reconciled, oldName, newName);
        renamed.add(oldName);
      }
    });
  }
  for (const param of previous) {
    if (param.name && !renamed.has(param.name) && !nextNames.has(param.name)) {
      reconciled = stripParamReferences(reconciled, param.name);
    }
  }
  return {
    search: reconciled,
    params: syncAllNativeFields(reconciled, next, search),
  };
}

export function reconcileSearchParamMappings({
  previousSearch,
  nextSearch,
  params,
}: {
  previousSearch: EsSearch;
  nextSearch: EsSearch;
  params: ParamDraft[];
}): ParamMappingEdit {
  return {
    search: nextSearch,
    params: syncAllNativeFields(nextSearch, params, previousSearch),
  };
}

const operands: ParamOperand[] = ["value", "values", "gt", "gte", "lt", "lte"];

function namedParam(params: ParamDraft[], name: string): ParamDraft {
  const param = params.find((candidate) => candidate.name === name);
  if (!param) throw new Error(`parameter ${name} does not exist`);
  return param;
}

function appendCondition(search: EsSearch, condition: EsCondition): EsSearch {
  if (!search.query || search.query.op === "match_all") {
    return { ...search, query: { ...emptyGroup(), conditions: [condition] } };
  }
  if (search.query.op === "bool") {
    return {
      ...search,
      query: {
        ...search.query,
        conditions: [...(search.query.conditions ?? []), condition],
      },
    };
  }
  return {
    ...search,
    query: { ...emptyGroup(), conditions: [search.query, condition] },
  };
}

function stripParamReferences(
  search: EsSearch,
  name: string,
  keepPath?: ConditionPath,
): EsSearch {
  const query = stripCondition(search.query, name, [], keepPath);
  return { ...search, query: query ?? emptyGroup() };
}

function stripCondition(
  condition: EsCondition | undefined,
  name: string,
  path: ConditionPath,
  keepPath?: ConditionPath,
): EsCondition | undefined {
  if (!condition) return undefined;
  if (keepPath && samePath(path, keepPath)) return condition;
  if (condition.when === name) return undefined;
  let next = removeReference(condition, name);
  if (!next) return undefined;
  if (next.conditions) {
    const conditions = next.conditions.flatMap((child, index) => {
      const stripped = stripCondition(child, name, [...path, index], keepPath);
      return stripped ? [stripped] : [];
    });
    next = { ...next, conditions };
    if (conditions.length === 0 && (condition.conditions?.length ?? 0) > 0)
      return undefined;
  }
  return next;
}

function removeReference(
  condition: EsCondition,
  name: string,
): EsCondition | undefined {
  let removed = false;
  const next = { ...condition };
  for (const operand of operands) {
    const value = condition[operand];
    if (operand === "values") {
      const values = (condition.values ?? []).filter((entry) => {
        const matches = isParamValue(entry) && entry.param === name;
        removed ||= matches;
        return !matches;
      });
      next.values = values.length ? values : undefined;
    } else if (isParamValue(value) && value.param === name) {
      next[operand] = undefined;
      removed = true;
    }
  }
  return removed && !hasOperand(next) ? undefined : next;
}

function hasOperand(condition: EsCondition): boolean {
  return (
    condition.value !== undefined ||
    Boolean(condition.values?.length) ||
    condition.gt !== undefined ||
    condition.gte !== undefined ||
    condition.lt !== undefined ||
    condition.lte !== undefined ||
    Boolean(condition.conditions?.length)
  );
}

function editAt(
  condition: EsCondition,
  path: ConditionPath,
  edit: (condition: EsCondition) => EsCondition | undefined,
): EsCondition | undefined {
  if (path.length === 0) return edit(condition);
  const [target, ...rest] = path;
  const children = condition.conditions ?? [];
  if (!children[target])
    throw new Error(`condition path ${path.join(".")} does not exist`);
  const edited = editAt(children[target], rest, edit);
  return {
    ...condition,
    conditions: children.flatMap((child, index) =>
      index !== target ? [child] : edited ? [edited] : [],
    ),
  };
}

function renameParamReferences(
  search: EsSearch,
  oldName: string,
  newName: string,
): EsSearch {
  const rename = (condition: EsCondition): EsCondition => {
    const next = { ...condition };
    for (const operand of operands) {
      const value = condition[operand];
      if (operand === "values") {
        next.values = condition.values?.map((entry) =>
          renamedValue(entry, oldName, newName),
        );
      } else {
        next[operand] = renamedValue(value, oldName, newName) as never;
      }
    }
    if (condition.when === oldName) next.when = newName;
    if (condition.conditions)
      next.conditions = condition.conditions.map(rename);
    return next;
  };
  return { ...search, query: search.query ? rename(search.query) : undefined };
}

function renamedValue(
  value: EsValue,
  oldName: string,
  newName: string,
): EsValue {
  return isParamValue(value) && value.param === oldName
    ? { param: newName }
    : value;
}

function syncNativeField(
  params: ParamDraft[],
  name: string,
  field: string | undefined,
): ParamDraft[] {
  const param = namedParam(params, name);
  if (param.type !== "list") return params;
  return params.map((candidate) => {
    if (candidate.name !== name) return candidate;
    const next: ParamDraft = { ...candidate, field };
    if (!field) delete next.field;
    return next;
  });
}

function syncAllNativeFields(
  search: EsSearch,
  params: ParamDraft[],
  previousSearch: EsSearch,
): ParamDraft[] {
  return params.map((param) => {
    if (param.type !== "list" || !param.name) return param;
    const mapping = paramMappings(search, param.name)[0];
    const wasMapped = paramMappings(previousSearch, param.name).length > 0;
    if (!mapping && !wasMapped) return param;
    const field = mapping?.field;
    if (field === param.field) return param;
    const next: ParamDraft = { ...param, field };
    if (!field) delete next.field;
    return next;
  });
}

function samePath(left: ConditionPath, right: ConditionPath): boolean {
  return (
    left.length === right.length &&
    left.every((value, index) => value === right[index])
  );
}
