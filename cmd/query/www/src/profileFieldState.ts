import { useMemo, useState } from "react";
import {
  applyVisibleFieldSelection,
  filterProfileFields,
  patchProfileField,
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
  const available = useMemo(() => {
    const discoveredNames = new Set(discovered.map((field) => field.name));
    return [
      ...discovered,
      ...configured.filter((field) => !discoveredNames.has(field.name)),
    ];
  }, [configured, discovered]);
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
    const updated = patchProfileField(field, patch);
    const exists = configured.some((entry) => entry.name === field.name);
    onConfiguredChange(
      exists
        ? configured.map((entry) => (entry.name === field.name ? updated : entry))
        : [...configured, updated],
    );
    if (typeof patch.name === "string") onActiveNameChange(patch.name);
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

  const moveActive = (offset: number) => {
    const target = activeIndex + offset;
    if (activeIndex < 0 || target < 0 || target >= configured.length) return;
    const next = [...configured];
    [next[activeIndex], next[target]] = [next[target], next[activeIndex]];
    onConfiguredChange(next);
  };

  const removeActive = () => {
    if (!activeField) return;
    const next = configured.filter((field) => field.name !== activeField.name);
    onConfiguredChange(next);
    onActiveNameChange(next[Math.max(0, activeIndex - 1)]?.name ?? "");
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
    removeActive,
  };
}
