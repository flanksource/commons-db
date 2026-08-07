import { useMemo, useState } from "react";
import {
  applyVisibleFieldSelection,
  availableProfileFields,
  filterProfileFields,
  patchProfileField,
  renameProfileField,
  reorderProfileColumns,
  type ProfileColumn,
  type ProfileFieldFilter,
} from "./profileWizardModel";

export type ProfileFieldStateProps = {
  discovered: ProfileColumn[];
  configured: ProfileColumn[];
  activeName: string;
  onConfiguredChange: (columns: ProfileColumn[]) => void;
  onActiveNameChange: (name: string) => void;
};

export type ProfileFieldState = ReturnType<typeof useProfileFieldState>;

/**
 * Column editing shared by the wizard's fields step and the editor route.
 *
 * The route splits the list and the inspector into separate Workspace panes,
 * which cannot each own this state, so it lives here and both surfaces read the
 * same derivations rather than re-deriving selection and filtering apart.
 */
export function useProfileFieldState({
  discovered,
  configured,
  activeName,
  onConfiguredChange,
  onActiveNameChange,
}: ProfileFieldStateProps) {
  const [filter, setFilter] = useState<ProfileFieldFilter>({
    query: "",
    type: "",
    selection: "all",
  });
  const available = useMemo(
    () => availableProfileFields(discovered, configured),
    [configured, discovered],
  );
  const selectedNames = useMemo(
    () => new Set(configured.map((field) => field.name)),
    [configured],
  );
  const visibleFields = useMemo(
    () => filterProfileFields(available, selectedNames, filter),
    [available, filter, selectedNames],
  );
  const types = useMemo(
    () =>
      Array.from(
        new Set(available.map((field) => field.type).filter(Boolean)),
      ).sort() as string[],
    [available],
  );
  const activeField =
    configured.find((field) => field.name === activeName) ??
    available.find((field) => field.name === activeName) ??
    configured[0] ??
    available[0];
  const activeIndex = activeField
    ? configured.findIndex((field) => field.name === activeField.name)
    : -1;

  const setVisibleSelection = (selected: boolean) => {
    onConfiguredChange(
      applyVisibleFieldSelection(
        available,
        configured,
        new Set(visibleFields.map((field) => field.name)),
        selected,
      ),
    );
  };

  const setFieldSelection = (field: ProfileColumn, selected: boolean) => {
    onConfiguredChange(
      applyVisibleFieldSelection(
        available,
        configured,
        new Set([field.name]),
        selected,
      ),
    );
  };

  const patchField = (field: ProfileColumn, patch: Partial<ProfileColumn>) => {
    const { name, ...properties } = patch;
    const updated =
      typeof name === "string"
        ? patchProfileField(renameProfileField(field, name), properties)
        : patchProfileField(field, patch);
    const exists = configured.some((entry) => entry.name === field.name);
    onConfiguredChange(
      exists
        ? configured.map((entry) => (entry.name === field.name ? updated : entry))
        : [...configured, updated],
    );
    if (typeof name === "string") onActiveNameChange(name);
  };

  const updateActiveField = (patch: Partial<ProfileColumn>) => {
    if (activeField) patchField(activeField, patch);
  };

  const addField = () => {
    const names = new Set(available.map((field) => field.name));
    let index = configured.length + 1;
    let name = `column_${index}`;
    while (names.has(name)) name = `column_${++index}`;
    onConfiguredChange([...configured, { name, type: "string" }]);
    onActiveNameChange(name);
  };

  const moveField = (field: ProfileColumn, offset: number) => {
    const from = configured.findIndex((entry) => entry.name === field.name);
    const target = from + offset;
    if (from < 0 || target < 0 || target >= configured.length) return;
    const next = [...configured];
    [next[from], next[target]] = [next[target], next[from]];
    onConfiguredChange(next);
  };

  const moveActive = (offset: number) => {
    if (activeField) moveField(activeField, offset);
  };

  /**
   * Drops the dragged field onto the target's position. Only configured fields
   * have a position to move: an unselected field is anchored to the sample, so
   * it is neither a source nor a destination.
   */
  const reorderField = (sourceName: string, targetName: string) => {
    const next = reorderProfileColumns(configured, sourceName, targetName);
    if (next !== configured) onConfiguredChange(next);
  };

  const removeField = (field: ProfileColumn) => {
    const removedIndex = configured.findIndex((entry) => entry.name === field.name);
    if (removedIndex < 0) return;
    const next = configured.filter((entry) => entry.name !== field.name);
    onConfiguredChange(next);
    if (activeField?.name === field.name) {
      onActiveNameChange(next[Math.max(0, removedIndex - 1)]?.name ?? "");
    }
  };

  const removeActive = () => {
    if (activeField) removeField(activeField);
  };

  return {
    available,
    selectedNames,
    visibleFields,
    types,
    filter,
    activeField,
    activeIndex,
    canMoveUp: activeIndex > 0,
    canMoveDown: activeIndex >= 0 && activeIndex < configured.length - 1,
    configuredCount: configured.length,
    setFilter,
    setActiveName: onActiveNameChange,
    setVisibleSelection,
    setFieldSelection,
    patchField,
    updateActiveField,
    addField,
    moveActive,
    moveField,
    reorderField,
    removeField,
    removeActive,
  };
}
