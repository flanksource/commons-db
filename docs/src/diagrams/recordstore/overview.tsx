// recordstore overview: how a captured stream reaches an engine, and how any
// engine becomes pageable through a sqlite index the profile engine reads.
import React from 'react';
import { Diagram, BoxNode, Arrow, COLORS } from '@flanksource/facet';
import { Redis, Valkey } from '@flanksource/icons/mi';
import { SiSqlite } from 'react-icons/si';
import { HiBell, HiPlay, HiArrowsRightLeft, HiDocumentText, HiCircleStack, HiTableCells } from 'react-icons/hi2';
import { HeaderTitle, IconNode, StepList, Facts, FlowLabel } from './parts';

export function StreamFlow() {
  return (
    <Diagram className="relative py-6">
      {(id) => (
        <>
          <div className="grid justify-center items-center justify-items-center gap-x-9 gap-y-6"
            style={{ gridTemplateColumns: 'repeat(6, max-content)', gridTemplateRows: 'repeat(3, auto)' }}>
            <div className="row-span-3">
              <BoxNode id={id('capture')} title={<HeaderTitle icon={HiPlay}>Capture</HeaderTitle>}
                headerColor={COLORS.primary} bodyColor={COLORS.background} compact minWidth="130px">
                <Facts title="Writer" items={['AppendTyped(stream,', '  kind, rows)', 'one per stream']} />
              </BoxNode>
            </div>
            <div className="row-span-3">
              <BoxNode id={id('notifier')} title={<HeaderTitle icon={HiBell}>Notifier</HeaderTitle>}
                headerColor={COLORS.primary} bodyColor={COLORS.background} compact minWidth="160px"
                ports={[{ position: 'bottom-middle', label: 'Tail · Wait → followers', color: COLORS.muted }]}>
                <Facts title="Wraps the source" items={['Append wakes waiters', 'on commit', 'recheck notices trim', 'and expiry']} />
              </BoxNode>
            </div>

            <BoxNode id={id('kv')} title={<HeaderTitle icon={HiArrowsRightLeft}>Router → kv</HeaderTitle>}
              headerColor={COLORS.accent} bodyColor={COLORS.background} borderColor={COLORS.accent} compact minWidth="165px">
              <div className="flex flex-col gap-1">
                <Facts title="Per tenant" items={['Route(ctx) → route', 'Open once, keep']} />
                <div className="flex items-center gap-1.5 text-[10px]" style={{ color: COLORS.accent }}>
                  <HiCircleStack className="w-4 h-4" /><Valkey className="w-4 h-4" /><Redis className="w-4 h-4" /> cache.Store
                </div>
              </div>
            </BoxNode>
            <div className="row-span-2">
              <IconNode id={id('indexer')} icon={HiArrowsRightLeft} label="Indexer.Ensure" caption="incremental by seq" />
            </div>
            <div className="row-span-2">
              <BoxNode id={id('index')} title={<HeaderTitle icon={SiSqlite}>index.sqlite</HeaderTitle>}
                headerColor={COLORS.primary} bodyColor={COLORS.background} compact minWidth="200px">
                <div className="flex flex-col gap-1.5">
                  <StepList steps={[
                    { label: 'Prepare', detail: 'drop an older generation' },
                    { label: 'Import', detail: 'rows after indexed seq, 500/batch' },
                    { label: 'TrimBelow', detail: 'mirror the source low seq' },
                    { label: 'SetExpiry', detail: 'mirror the source expiry' },
                  ]} />
                  <Facts title="Derived" items={['TTL 0 · dropped and rebuilt', 'from the source on change']} />
                </div>
              </BoxNode>
            </div>
            <div className="row-span-3">
              <BoxNode id={id('registry')} title={<HeaderTitle icon={HiTableCells}>Registry</HeaderTitle>}
                headerColor={COLORS.outputBorder} bodyColor={COLORS.background} borderColor={COLORS.outputBorder} compact minWidth="150px">
                <Facts title="sql profiles" items={['read via ReadDSN', 'page · filter · export', 'follow via Notifier']} />
              </BoxNode>
            </div>

            <BoxNode id={id('ndjson')} title={<HeaderTitle icon={HiDocumentText}>ndjson/</HeaderTitle>}
              headerColor={COLORS.accent} bodyColor={COLORS.background} borderColor={COLORS.accent} compact minWidth="165px">
              <Facts title="Local files" items={['no kv store', 'MaxBytes · KeepStreams']} />
            </BoxNode>

            <BoxNode id={id('records')} title={<HeaderTitle icon={SiSqlite}>records.sqlite</HeaderTitle>}
              headerColor={COLORS.accent} bodyColor={COLORS.background} borderColor={COLORS.accent} compact minWidth="165px">
              <Facts title="Local, durable" items={['its own index, read directly', 'Derived=false']} />
            </BoxNode>
          </div>

          <Arrow variant="primary" from={id('capture')} to={id('notifier')} path="straight" startAnchor="right" endAnchor="left" />
          <Arrow variant="primary" from={id('notifier')} to={id('kv')} startAnchor={{ position: 'right', offset: { y: -30 } }} endAnchor="left"
            labels={{ middle: <FlowLabel text="hasKV" /> }} />
          <Arrow variant="secondary" from={id('notifier')} to={id('ndjson')} path="straight" startAnchor="right" endAnchor="left" />
          <Arrow variant="secondary" from={id('notifier')} to={id('records')} startAnchor={{ position: 'right', offset: { y: 30 } }} endAnchor="left" />
          <Arrow variant="primary" from={id('kv')} to={id('indexer')} startAnchor="right" endAnchor={{ position: 'left', offset: { y: -10 } }}
            labels={{ middle: <FlowLabel text="Scan" /> }} />
          <Arrow variant="secondary" from={id('ndjson')} to={id('indexer')} startAnchor="right" endAnchor={{ position: 'left', offset: { y: 10 } }} />
          <Arrow variant="primary" from={id('indexer')} to={id('index')} path="straight" startAnchor="right" endAnchor="left" />
          <Arrow variant="primary" from={id('index')} to={id('registry')} startAnchor="right" endAnchor="left" />
          <Arrow variant="secondary" from={id('records')} to={id('registry')} startAnchor="right" endAnchor="bottom" />
        </>
      )}
    </Diagram>
  );
}

const openMatrix = [
  { source: 'nil', backend: 'sqlite', streams: 'records.sqlite', index: 'the same file' },
  { source: 'nil', backend: 'ndjson', streams: '<dir>/ndjson/', index: 'index.sqlite, derived' },
  { source: 'a Router (kv per tenant)', backend: 'any', streams: 'the Router', index: 'index.sqlite, derived' },
];

export function OpenMatrix() {
  const unskinned: React.CSSProperties = { backgroundColor: 'transparent', border: 'none' };
  const cell: React.CSSProperties = { ...unskinned, color: COLORS.muted, fontSize: '10px', padding: '3px 10px', whiteSpace: 'nowrap', textAlign: 'left' };
  const header: React.CSSProperties = { ...cell, fontWeight: 700, borderBottom: `1px solid ${COLORS.outputBorder}` };
  return (
    <div className="flex justify-center py-4">
      <BoxNode title="recordresults.Open" headerColor={COLORS.outputBorder} bodyColor={COLORS.background}
        borderColor={COLORS.outputBorder} compact minWidth="620px">
        <table className="w-full" style={{ borderCollapse: 'collapse', margin: 0 }}>
          <thead style={unskinned}>
            <tr style={unskinned}>
              {['OpenOptions.Source', 'Settings.Backend', 'streams are written to', 'profiles read'].map((text) => (
                <th key={text} style={header}>{text}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {openMatrix.map((row) => (
              <tr key={row.source + row.backend} style={unskinned}>
                <td style={cell}>{row.source}</td>
                <td style={cell}>{row.backend}</td>
                <td style={{ ...cell, color: COLORS.accent, fontWeight: 600 }}>{row.streams}</td>
                <td style={cell}>{row.index}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </BoxNode>
    </div>
  );
}
