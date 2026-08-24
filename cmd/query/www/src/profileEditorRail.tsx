import {
  profileEditorSections,
  type ProfileEditorSection,
  type ProfileSectionStatus,
} from "./profileEditorModel";

/** Vertical section nav for the editor route, in place of the modal's tabs. */
export function ProfileEditorRail({
  value,
  status,
  onChange,
}: {
  value: ProfileEditorSection;
  status: Record<ProfileEditorSection, ProfileSectionStatus>;
  onChange: (section: ProfileEditorSection) => void;
}) {
  return (
    <nav className="w-full overflow-auto p-2" aria-label="Profile sections">
      {profileEditorSections.map((section) => {
        const active = section.id === value;
        const { badge, attention } = status[section.id];
        return (
          <button
            key={section.id}
            type="button"
            aria-current={active ? "page" : undefined}
            className={`mb-0.5 flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left transition-colors ${
              active ? "bg-background shadow-sm ring-1 ring-border" : "hover:bg-background/70"
            }`}
            onClick={() => onChange(section.id)}
          >
            <span className="min-w-0 flex-1">
              <span
                className={`block truncate text-sm ${active ? "font-medium text-foreground" : "text-foreground/80"}`}
              >
                {section.label}
              </span>
              <span className="block truncate text-[11px] text-muted-foreground">
                {section.hint}
              </span>
            </span>
            {attention ? (
              <span
                className="size-1.5 shrink-0 rounded-full bg-warning"
                title="Needs attention"
              />
            ) : null}
            {badge ? (
              <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                {badge}
              </span>
            ) : null}
          </button>
        );
      })}
    </nav>
  );
}
