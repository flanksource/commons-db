/**
 * One leaf condition: which field, which operator, and the operand that
 * operator takes. Which operators a field may use and which advanced qualifiers
 * an operator emits both come from the schema vocabulary, so this file owns the
 * editing surface and never the vocabulary itself.
 */

import {
  Combobox,
  IconButton,
  MultiSelect,
  Select,
} from "@flanksource/clicky-ui";
import { UiTrash } from "@flanksource/clicky-ui/icons";
import type { ConditionPath, EsCondition } from "./esQueryBuilderModel";
import type { ParamOperand } from "./esParamMappingModel";
import type { FieldValuesQuery } from "./esFieldValues";
import { ConditionOperand, QualifierInput } from "./esQueryOperandEditors";
import type { ParamDraft } from "./profileWizardModel";
import {
  changeConditionOperator,
  fieldWarning,
  operatorsForField,
  qualifiersForOperator,
  type EsBuilderVocabulary,
  type EsFieldMapping,
} from "./esQueryOperators";

export type EsQueryContext = {
  fields: EsFieldMapping[];
  vocabulary: EsBuilderVocabulary;
  /** Declared profile parameters an operand can bind to. */
  params: ParamDraft[];
  /**
   * The values a row's field holds, scoped by the builder to the rest of the
   * query. Absent where the host has no connection to ask.
   */
  values?: (request: {
    path: ConditionPath;
    field: string;
  }) => FieldValuesQuery | undefined;
};

/**
 * The edits a condition row or group asks for, addressed by path. The builder
 * owns the tree and applies them; every node below it is stateless.
 */
export type EsQueryTreeActions = {
  update: (
    path: ConditionPath,
    update: (condition: EsCondition) => EsCondition,
  ) => void;
  insert: (groupPath: ConditionPath, condition: EsCondition) => void;
  remove: (path: ConditionPath) => void;
  mapParam: (path: ConditionPath, operand: ParamOperand, name: string) => void;
  unmapParam: (path: ConditionPath, name: string) => void;
};

// How each bool clause reads to an author. filter and must both narrow, but
// only must scores, which is the distinction the raw names hide.
const occurLabels: Record<string, string> = {
  filter: "AND",
  must: "AND (scored)",
  should: "OR",
  must_not: "NOT",
};

export function occurOptions(occurs: string[]) {
  return occurs.map((occur) => ({
    value: occur,
    label: occurLabels[occur] ?? occur,
  }));
}

export function EsQueryConditionRow({
  condition,
  path,
  context,
  actions,
}: {
  condition: EsCondition;
  path: ConditionPath;
  context: EsQueryContext;
  actions: EsQueryTreeActions;
}) {
  const { catalog, qualifierNames, qualifierRestrictions, qualifiers } =
    context.vocabulary;
  const field = context.fields.find((entry) => entry.name === condition.field);
  const info = catalog.find((entry) => entry.op === condition.op);
  const warning = field ? fieldWarning(field) : undefined;
  const set = (patch: Partial<EsCondition>) =>
    actions.update(path, (current) => ({ ...current, ...patch }));
  const rowId = path.join("-") || "root";
  const values = condition.field
    ? context.values?.({ path, field: condition.field })
    : undefined;

  const advanced = qualifiersForOperator({
    names: qualifierNames,
    ...(qualifierRestrictions ? { restrictions: qualifierRestrictions } : {}),
    op: condition.op,
  });

  return (
    <div className="rounded-md border bg-card px-2 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="w-28 shrink-0">
          <Select
            aria-label="Clause"
            value={condition.occur || "filter"}
            options={occurOptions(context.vocabulary.occurs)}
            onChange={(event) => set({ occur: event.target.value as never })}
          />
        </span>
        {info?.needsField ? (
          <Combobox
            ariaLabel="Field"
            className="min-w-56 flex-1"
            value={condition.field ?? ""}
            onChange={(next) => set({ field: next })}
            options={fieldOptions(context.fields)}
            placeholder="Field…"
          />
        ) : null}
        {info?.acceptsFields ? (
          <MultiSelect
            ariaLabel="Fields"
            className="min-w-56 flex-1"
            value={condition.fields ?? []}
            onChange={(next) => set({ fields: next })}
            options={context.fields.map((entry) => ({
              value: entry.name,
              label: entry.name,
            }))}
            placeholder="All fields"
          />
        ) : null}
        <span className="w-48 shrink-0">
          <Select
            aria-label="Operator"
            value={condition.op}
            onChange={(event) => {
              const next = catalog.find(
                (entry) => entry.op === event.target.value,
              );
              if (!next) {
                throw new Error(
                  `operator ${event.target.value} is missing from the catalog`,
                );
              }
              actions.update(path, (current) =>
                changeConditionOperator(current, next),
              );
            }}
          >
            {operatorGroups(catalog, field, condition.op).map((group) => {
              const options = group.operators.map((entry) => (
                <option key={entry.op} value={entry.op}>
                  {entry.label}
                </option>
              ));
              return group.label ? (
                <optgroup key={group.label} label={group.label}>
                  {options}
                </optgroup>
              ) : (
                options
              );
            })}
          </Select>
        </span>
        <ConditionOperand
          condition={condition}
          arity={info?.arity ?? "single"}
          rowId={rowId}
          params={context.params}
          dateMath={Boolean(
            field?.dataType === "date" || field?.types?.includes("date"),
          )}
          {...(values ? { values } : {})}
          set={set}
          onBindParam={(operand, name) => actions.mapParam(path, operand, name)}
          onUnbindParam={(name) => actions.unmapParam(path, name)}
        />
        <IconButton
          icon={UiTrash}
          label="Remove condition"
          onClick={() => actions.remove(path)}
        />
      </div>
      {warning ? (
        <p role="alert" className="mt-1 text-xs text-amber-600 dark:text-amber-400">
          {warning}
        </p>
      ) : null}
      {advanced.length ? (
        <details className="mt-1">
          <summary className="cursor-pointer text-xs text-muted-foreground">
            Advanced
          </summary>
          <div className="mt-2 flex flex-wrap items-end gap-2">
            {advanced.map((name) => (
              <QualifierInput
                key={name}
                name={name}
                schema={qualifiers[name] ?? {}}
                value={(condition as Record<string, unknown>)[name]}
                onChange={(next) => set({ [name]: next } as Partial<EsCondition>)}
              />
            ))}
            <label className="flex items-center gap-1 text-xs text-muted-foreground">
              <input
                type="checkbox"
                checked={condition.optional === true}
                onChange={(event) => set({ optional: event.target.checked })}
              />
              Optional
            </label>
          </div>
        </details>
      ) : null}
    </div>
  );
}

function fieldOptions(fields: EsFieldMapping[]) {
  return fields.map((field) => ({
    value: field.name,
    label: field.name,
    title: (field.types ?? (field.dataType ? [field.dataType] : [])).join(", "),
  }));
}

type OperatorGroup = {
  label?: string;
  operators: { op: string; label: string }[];
};

/**
 * operatorGroups splits the offered operators into the ones that suit the field
 * and the ones that fight its analysis, which render behind an Advanced
 * divider. The condition's own operator is always offered, even where the field
 * does not suit it, so changing the field never blanks the control.
 */
function operatorGroups(
  catalog: EsBuilderVocabulary["catalog"],
  field: EsFieldMapping | undefined,
  current: string,
): OperatorGroup[] {
  const offered = operatorsForField(catalog, field);
  const groups: OperatorGroup[] = [];
  const plain = offered.filter((entry) => !entry.advanced);
  const advanced = offered.filter((entry) => entry.advanced);
  if (!offered.some((entry) => entry.op === current)) {
    const info = catalog.find((entry) => entry.op === current);
    advanced.unshift({ ...(info ?? { op: current, label: current, arity: "single", fieldTypes: [] }) });
  }
  if (plain.length) groups.push({ operators: plain });
  if (advanced.length) groups.push({ label: "Advanced", operators: advanced });
  return groups;
}
