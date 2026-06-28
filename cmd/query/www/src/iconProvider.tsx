import type { ElementType } from "react";
import { IconMap } from "@flanksource/icons/mi";
import type { FallbackIconProps } from "@flanksource/clicky-ui";

// FlanksourceIcon resolves a runtime icon name against @flanksource/icons' name
// map and renders the matching glyph. It is registered as clicky-ui's fallback
// icon provider (see main.tsx) so name-based icons — e.g. the connection-type
// picker grid (x-enum-icons) — render. An unknown name renders nothing, so the
// grid gracefully falls back to the option's text label.
const icons = IconMap as unknown as Record<string, ElementType>;

export function FlanksourceIcon({ name, className, size }: FallbackIconProps) {
  const Glyph = name ? icons[name] : undefined;
  if (!Glyph) return null;
  return <Glyph className={className} {...(size != null ? { width: size, height: size } : {})} />;
}
