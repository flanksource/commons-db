// Shared layout for the capture examples: one producer appending one kind,
// fanned out to the two backends Settings.Resolve picks between, and the
// profile both are read through. Each example fills the boxes with what that
// capture actually stores.
import React from 'react';
import { Diagram, BoxNode, Arrow, COLORS } from '@flanksource/facet';
import { SiSqlite, SiRedis } from 'react-icons/si';
import { HiArrowsRightLeft, HiTableCells } from 'react-icons/hi2';
import { HeaderTitle, StepList, Facts, FieldRow, FlowLabel, type Step, type FieldDef, type IconComponent } from './parts';

const mono: React.CSSProperties = { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' };

// Path breaks a long key or file path only after its / and : separators.
export function Path({ children }: { children: string }) {
  return (
    <div className="text-[9px] break-words leading-snug" style={{ ...mono, color: COLORS.muted }}>
      {children.replace(/([/:])/g, '$1​')}
    </div>
  );
}

export interface StoredKey {
  key: string;
  type: string;
  value: string;
}

// KeyList is a kv stream as its keys under the stream prefix.
export function KeyList({ prefix, keys }: { prefix: string; keys: StoredKey[] }) {
  return (
    <div className="flex flex-col gap-1">
      <Path>{prefix}</Path>
      {keys.map((entry) => (
        <div key={entry.key} className="rounded px-1.5 py-0.5" style={{ border: `1px solid ${COLORS.primary}33` }}>
          <div className="flex items-center gap-1">
            <span className="text-[9px] font-semibold break-all" style={{ ...mono, color: COLORS.accent }}>{entry.key}</span>
            <span className="text-[8px] font-bold ml-auto pl-1" style={{ color: COLORS.muted }}>{entry.type}</span>
          </div>
          <div className="text-[9px] leading-snug" style={{ ...mono, color: COLORS.muted }}>{entry.value}</div>
        </div>
      ))}
    </div>
  );
}

// SampleRows is a few rows of a records_<kind> table, stream and seq first.
export function SampleRows({ table, columns, rows }: { table: string; columns: string[]; rows: string[][] }) {
  const unskinned: React.CSSProperties = { backgroundColor: 'transparent', border: 'none' };
  const cell: React.CSSProperties = { ...unskinned, ...mono, color: COLORS.muted, fontSize: '9px', padding: '1px 4px', whiteSpace: 'nowrap', textAlign: 'left' };
  const header: React.CSSProperties = { ...cell, fontWeight: 700, color: COLORS.accent, borderBottom: `1px solid ${COLORS.primary}` };
  return (
    <div className="flex flex-col gap-0.5">
      <div className="text-[9px] font-bold" style={{ ...mono, color: COLORS.accent }}>{table}</div>
      <table style={{ borderCollapse: 'collapse', margin: 0 }}>
        <thead style={unskinned}>
          <tr style={unskinned}>{columns.map((column) => <th key={column} style={header}>{column}</th>)}</tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.join('|')} style={unskinned}>{row.map((value, index) => <td key={index} style={cell}>{value}</td>)}</tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export interface CaptureStoreProps {
  producer: { title: string; icon: IconComponent; source: React.ReactNode; steps: Step[]; note?: React.ReactNode };
  contract: { kind: string; declared: FieldDef[]; columns: FieldDef[] };
  sqlite: React.ReactNode;
  kv: React.ReactNode;
  index: string[];
  reader: { profile: string; items: string[] };
}

export function CaptureStoreDiagram({ producer, contract, sqlite, kv, index, reader }: CaptureStoreProps) {
  return (
    <Diagram className="relative py-6">
      {(id) => (
        <>
          <div className="grid justify-center items-center justify-items-center"
            style={{ gridTemplateColumns: 'repeat(4, max-content)', gridTemplateRows: 'repeat(2, auto)', columnGap: '56px', rowGap: '44px' }}>
            <div className="row-span-2">
              <BoxNode id={id('producer')} title={<HeaderTitle icon={producer.icon}>{producer.title}</HeaderTitle>}
                headerColor={COLORS.primary} bodyColor={COLORS.background} compact minWidth="220px">
                <div className="flex flex-col gap-2" style={{ maxWidth: '220px' }}>
                  <div className="flex items-center gap-1.5 text-[10px] font-semibold" style={{ color: COLORS.accent }}>{producer.source}</div>
                  <StepList steps={producer.steps} />
                  {producer.note}
                </div>
              </BoxNode>
            </div>
            <div className="row-span-2">
              <BoxNode id={id('contract')} title={`kind ${contract.kind}`}
                headerColor={COLORS.accent} bodyColor={COLORS.background} borderColor={COLORS.accent} compact minWidth="200px">
                <div className="flex flex-col gap-1" style={{ maxWidth: '220px' }}>
                  <div className="text-[9px] font-bold uppercase tracking-wide" style={{ color: COLORS.muted }}>Declared</div>
                  {contract.declared.map((field) => <FieldRow key={field.name} {...field} />)}
                  <div className="text-[9px] font-bold uppercase tracking-wide mt-1" style={{ color: COLORS.muted }}>Columns</div>
                  {contract.columns.map((field) => <FieldRow key={field.name} {...field} />)}
                </div>
              </BoxNode>
            </div>
            <BoxNode id={id('sqlite')} title={<HeaderTitle icon={SiSqlite}>records.sqlite</HeaderTitle>}
              headerColor={COLORS.primary} bodyColor={COLORS.background} compact minWidth="300px"
              ports={[{ position: 'top-left', label: 'no valkey L2', color: COLORS.muted }]}>
              <div className="flex flex-col gap-1.5" style={{ maxWidth: '300px' }}>{sqlite}</div>
            </BoxNode>
            <BoxNode id={id('reader')} title={<HeaderTitle icon={HiTableCells}>profile</HeaderTitle>}
              headerColor={COLORS.outputBorder} bodyColor={COLORS.background} borderColor={COLORS.outputBorder} compact minWidth="180px">
              <div className="flex flex-col gap-0.5" style={{ maxWidth: '190px' }}>
                <div className="text-[10px] font-bold" style={{ color: COLORS.outputBorder }}>{reader.profile}</div>
                {reader.items.map((item) => (
                  <div key={item} className="text-[10px] leading-snug" style={{ color: COLORS.accent }}>{item}</div>
                ))}
              </div>
            </BoxNode>
            <BoxNode id={id('kv')} title={<HeaderTitle icon={SiRedis}>valkey · kv</HeaderTitle>}
              headerColor={COLORS.primary} bodyColor={COLORS.background} compact minWidth="300px"
              ports={[{ position: 'top-left', label: 'valkey L2', color: COLORS.muted }]}>
              <div className="flex flex-col gap-1.5" style={{ maxWidth: '300px' }}>{kv}</div>
            </BoxNode>
            <BoxNode id={id('index')} title={<HeaderTitle icon={SiSqlite}>index.sqlite</HeaderTitle>}
              headerColor={COLORS.muted} bodyColor={COLORS.background} borderColor={COLORS.muted} compact minWidth="180px">
              <div style={{ maxWidth: '190px' }}>
                <Facts title="Derived, per read" items={index} />
              </div>
            </BoxNode>
          </div>

          <Arrow variant="primary" from={id('producer')} to={id('contract')} path="straight" startAnchor="right" endAnchor="left" />
          <Arrow variant="primary" from={id('contract')} to={id('sqlite')} startAnchor={{ position: 'right', offset: { y: -40 } }} endAnchor="left" />
          <Arrow variant="primary" from={id('contract')} to={id('kv')} startAnchor={{ position: 'right', offset: { y: 40 } }} endAnchor="left" />
          <Arrow variant="primary" from={id('sqlite')} to={id('reader')} path="straight" startAnchor="right" endAnchor="left" />
          <Arrow variant="secondary" from={id('kv')} to={id('index')} path="straight" startAnchor="right" endAnchor="left" />
          <Arrow variant="secondary" from={id('index')} to={id('reader')} path="straight" startAnchor="top" endAnchor="bottom"
            labels={{ middle: <FlowLabel text="Indexer.Ensure" /> }} />
        </>
      )}
    </Diagram>
  );
}

export interface Comparison {
  aspect: string;
  sqlite: string;
  kv: string;
}

// StoreComparison sets what one capture meets in each backend side by side.
export function StoreComparison({ title, rows }: { title: string; rows: Comparison[] }) {
  const unskinned: React.CSSProperties = { backgroundColor: 'transparent', border: 'none' };
  const cell: React.CSSProperties = { ...unskinned, color: COLORS.muted, fontSize: '10px', padding: '4px 10px', textAlign: 'left', verticalAlign: 'top', lineHeight: 1.35 };
  const header: React.CSSProperties = { ...cell, fontWeight: 700, color: COLORS.accent, borderBottom: `1px solid ${COLORS.accent}` };
  return (
    <div className="flex justify-center py-4">
      <BoxNode title={<HeaderTitle icon={HiArrowsRightLeft}>{title}</HeaderTitle>} headerColor={COLORS.accent}
        bodyColor={COLORS.background} borderColor={COLORS.accent} compact minWidth="900px">
        <table style={{ borderCollapse: 'collapse', margin: 0, width: '900px' }}>
          <colgroup>
            <col style={{ width: '150px' }} />
            <col style={{ width: '375px' }} />
            <col style={{ width: '375px' }} />
          </colgroup>
          <thead style={unskinned}>
            <tr style={unskinned}>
              <th style={header} />
              <th style={header}>records.sqlite</th>
              <th style={header}>valkey · kv</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.aspect} style={unskinned}>
                <td style={{ ...cell, fontWeight: 700, color: COLORS.accent }}>{row.aspect}</td>
                <td style={cell}>{row.sqlite}</td>
                <td style={cell}>{row.kv}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </BoxNode>
    </div>
  );
}
