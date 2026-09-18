// Shared recipes for the recordstore diagrams (diagram-designer skill):
// borderless intermediaries, entity field rows, cardinality pills and the
// numbered step list an engine box uses to show one call's path.
import React from 'react';
import { BoxNode, COLORS } from '@flanksource/facet';

export type IconComponent = React.ComponentType<{ className?: string; style?: React.CSSProperties }>;

export function HeaderTitle({ icon: Icon, children }: { icon: IconComponent; children: React.ReactNode }) {
  return (
    <span className="flex items-center justify-center gap-2">
      <Icon className="w-4 h-4 text-white" />
      {children}
    </span>
  );
}

export function IconNode({ id, icon: Icon, label, caption }: {
  id: string; icon: IconComponent; label: string; caption?: string;
}) {
  return (
    <div id={id} className="flex flex-col items-center gap-0.5 px-2 py-1">
      <Icon className="w-7 h-7" style={{ color: COLORS.accent }} />
      <span className="text-[10px] font-bold whitespace-nowrap" style={{ color: COLORS.muted }}>{label}</span>
      {caption && <span className="text-[9px] whitespace-nowrap" style={{ color: COLORS.muted }}>{caption}</span>}
    </div>
  );
}

export interface Step {
  label: string;
  detail?: string;
  commit?: boolean;
}

export function StepList({ title, steps, start = 1 }: { title?: string; steps: Step[]; start?: number }) {
  return (
    <div className="flex flex-col gap-1">
      {title && (
        <div className="text-[9px] font-bold uppercase tracking-wide" style={{ color: COLORS.muted }}>{title}</div>
      )}
      {steps.map((step, index) => (
        <div key={step.label} className="flex items-start gap-1.5 rounded px-1.5 py-0.5"
          style={step.commit ? { border: `1px solid ${COLORS.outputBorder}` } : { border: '1px solid transparent' }}>
          <span className="flex items-center justify-center shrink-0 rounded-full text-[9px] font-bold text-white"
            style={{ width: 15, height: 15, backgroundColor: step.commit ? COLORS.outputBorder : COLORS.primary }}>
            {start + index}
          </span>
          <span className="flex flex-col leading-tight">
            <span className="text-[10px] font-semibold" style={{ color: COLORS.accent }}>{step.label}</span>
            {step.detail && <span className="text-[9px]" style={{ color: COLORS.muted }}>{step.detail}</span>}
          </span>
        </div>
      ))}
    </div>
  );
}

export function Facts({ title, items }: { title: string; items: string[] }) {
  return (
    <div className="flex flex-col gap-0.5">
      <div className="text-[9px] font-bold uppercase tracking-wide" style={{ color: COLORS.muted }}>{title}</div>
      {items.map((item) => (
        <div key={item} className="text-[10px] leading-snug" style={{ color: COLORS.accent }}>{item}</div>
      ))}
    </div>
  );
}

export interface FieldDef {
  name: string;
  type: string;
  pk?: boolean;
  fk?: boolean;
  /** An index marker other than the primary key, e.g. "IDX" or "UQ". */
  index?: string;
}

function Badge({ text, color }: { text: string; color: string }) {
  return <span className="text-[8px] font-bold px-1 rounded text-white" style={{ backgroundColor: color }}>{text}</span>;
}

export function FieldRow({ name, type, pk, fk, index }: FieldDef) {
  const marked = pk || fk || index;
  return (
    <div className="flex items-center gap-1 py-0.5">
      {pk && <Badge text="PK" color={COLORS.pk} />}
      {fk && <Badge text="FK" color={COLORS.fk} />}
      {index && <Badge text={index} color={COLORS.accent} />}
      {!marked && <span className="w-[18px]" />}
      <span className="text-[10px] font-semibold whitespace-nowrap" style={{ color: COLORS.accent }}>{name}</span>
      <span className="text-[9px] ml-auto pl-3 whitespace-nowrap" style={{ color: COLORS.muted }}>{type}</span>
    </div>
  );
}

export function EntityBox({ id, title, fields, accent = COLORS.primary, note, minWidth = '190px' }: {
  id: string; title: React.ReactNode; fields: FieldDef[]; accent?: string; note?: string; minWidth?: string;
}) {
  return (
    <BoxNode id={id} title={title} headerColor={accent} bodyColor={COLORS.background}
      borderColor={accent} compact minWidth={minWidth}>
      <div className="flex flex-col">
        {fields.map((field) => <FieldRow key={field.name} {...field} />)}
        {note && (
          <div className="text-[9px] italic mt-1 pt-1 leading-snug"
            style={{ color: COLORS.muted, borderTop: `1px solid ${COLORS.muted}33`, maxWidth: minWidth }}>
            {note}
          </div>
        )}
      </div>
    </BoxNode>
  );
}

export function RelLabel({ text }: { text: string }) {
  return (
    <div className="text-[9px] font-semibold px-1 rounded whitespace-nowrap"
      style={{ backgroundColor: COLORS.background, color: COLORS.muted, border: `1px solid ${COLORS.muted}` }}>
      {text}
    </div>
  );
}

export function FlowLabel({ text }: { text: string }) {
  return (
    <div className="text-[9px] font-semibold px-1.5 py-0.5 rounded whitespace-nowrap text-white"
      style={{ backgroundColor: COLORS.primary }}>
      {text}
    </div>
  );
}
