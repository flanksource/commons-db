/**
 * The profile picker: browse the hierarchy encoded in profile names instead of
 * scrolling one flat, alphabetical list of every profile.
 *
 * Profile names are dotted (`jms.incoming.disbursements`), and a parent segment
 * is usually itself a real profile — so `jms` has to be selectable *and*
 * expandable. That is exactly what clicky-ui's path tree produces, which is why
 * this file is a thin adapter rather than a component.
 */

import { TreePickerField, buildPathTree, splitPath, type PathTreeNode } from "@flanksource/clicky-ui";
import { useMemo } from "react";

/**
 * The characters a profile name uses to separate levels. Mirrors
 * `profilePathDelimiters` in cmd/query/profiles/service.go and the `hierarchy`
 * declared on the profile lookups in query/schema/profile.go — a hyphen is
 * deliberately absent, or `remote-debugger` would split in two.
 */
export const PROFILE_PATH_DELIMITERS = "./";

type ProfileNode = PathTreeNode<string>;

function nameOf(node: ProfileNode): string | undefined {
  return node.items[0];
}

export function ProfilePicker({
  names,
  value,
  onChange,
  placeholder = "Pick a profile…",
  ariaLabel,
  disabled,
}: {
  names: string[];
  value: string;
  onChange: (name: string) => void;
  placeholder?: string;
  ariaLabel: string;
  disabled?: boolean;
}) {
  const roots = useMemo(
    () => buildPathTree(names, (name) => splitPath(name, PROFILE_PATH_DELIMITERS)),
    [names],
  );
  const selected = useMemo(() => findByName(roots, value), [roots, value]);

  return (
    <TreePickerField<ProfileNode>
      roots={roots}
      getKey={(node) => node.key}
      getChildren={(node) => node.children}
      getSearchText={(node) => node.key}
      // Closed by default: a pre-expanded first level would undo the point of
      // the tree with 55 profiles in the store.
      defaultOpen={() => false}
      renderRow={({ node }) => (
        <span className={nameOf(node) ? "truncate text-sm" : "truncate text-sm text-muted-foreground"}>
          {node.label}
        </span>
      )}
      isSelectable={(node) => nameOf(node) !== undefined}
      onSelect={(node) => {
        const name = nameOf(node);
        if (name) onChange(name);
      }}
      selected={selected}
      revealSelected
      ariaLabel={ariaLabel}
      placeholder={placeholder}
      {...(value ? { label: value } : {})}
      {...(disabled ? { disabled } : {})}
    />
  );
}

function findByName(nodes: ProfileNode[], name: string): ProfileNode | null {
  if (!name) return null;
  for (const node of nodes) {
    if (nameOf(node) === name) return node;
    const nested = findByName(node.children, name);
    if (nested) return nested;
  }
  return null;
}
