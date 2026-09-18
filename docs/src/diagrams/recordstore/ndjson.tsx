// recordstore/ndjson: each stream is a local newline-delimited JSON file beside
// a .meta.json sidecar that commits it. The durable store a CLI run writes when
// no shared store is configured.
import React from 'react';
import { Diagram, BoxNode, Arrow, NodeSection, COLORS } from '@flanksource/facet';
import { HiBell, HiMagnifyingGlass, HiLockClosed, HiArrowPath, HiFolder, HiDocumentText, HiCodeBracket } from 'react-icons/hi2';
import { HeaderTitle, IconNode, StepList, Facts, EntityBox, FieldRow, RelLabel, FlowLabel } from './parts';

export function NDJSONAppendFlow() {
  return (
    <Diagram className="relative py-6">
      {(id) => (
        <>
          <div className="flex items-center justify-center gap-5">
            <div className="flex flex-col gap-24">
              <BoxNode id={id('append')} title={<HeaderTitle icon={HiBell}>Notifier.Append</HeaderTitle>}
                headerColor={COLORS.primary} bodyColor={COLORS.background} compact minWidth="190px">
                <Facts title="Writer" items={['a CLI run, no shared store', 'wakes followers on commit']} />
              </BoxNode>
              <BoxNode id={id('scan')} title={<HeaderTitle icon={HiMagnifyingGlass}>Scan / Tail</HeaderTitle>}
                headerColor={COLORS.muted} bodyColor={COLORS.background} borderColor={COLORS.muted} compact minWidth="190px">
                <Facts title="Reader" items={['Indexer.Ensure → index.sqlite', 'resumes after a seq']} />
              </BoxNode>
            </div>

            <div className="flex flex-col items-center gap-2" style={{ minWidth: '150px' }}>
              <IconNode id={id('locks')} icon={HiLockClosed} label="StreamLocks" caption="serialize appends" />
              <IconNode id={id('opening')} icon={HiArrowPath} label="opening mutex" caption="create + rotate together" />
            </div>

            <BoxNode id={id('backend')} title={<HeaderTitle icon={HiDocumentText}>ndjson.Backend</HeaderTitle>}
              headerColor={COLORS.primary} bodyColor={COLORS.background} minWidth="280px">
              <div className="flex flex-col gap-2">
                <StepList title="Append, under the stream lock" steps={[
                  { label: 'find', detail: 'read <stream>.meta.json; expired → remove files' },
                  { label: 'openStream', detail: 'new stream: rotate the kind dir, write sidecar' },
                  { label: 'trimLocked', detail: 'RetainRows: copy kept lines to <stream>@<low>' },
                  { label: 'unstored', detail: 'keyed: skip keys already in sidecar.keys' },
                  { label: 'cap check', detail: 'bytes + batch > MaxBytes → capped, ErrCapacity' },
                  { label: 'appendData', detail: 'truncate to committed bytes, then WriteAt' },
                  { label: 'writeSidecar', detail: 'write .tmp, rename over: the commit point', commit: true },
                ]} />
                <Facts title="Scan" items={['lineAfter bisects the file by byte offset', 'reads to sidecar.bytes, checks every seq']} />
              </div>
            </BoxNode>

            <div style={{ minWidth: '70px' }} />

            <BoxNode id={id('dir')} title={<HeaderTitle icon={HiFolder}>{'<dir>/<kind>/'}</HeaderTitle>}
              headerColor={COLORS.accent} bodyColor={COLORS.background} borderColor={COLORS.accent} minWidth="220px">
              <div className="flex flex-col gap-2">
                <NodeSection title="Per stream" items={['<stream>.meta.json', '<stream>.ndjson', '<stream>@<low>.ndjson']} />
                <Facts title="Bounds" items={['MaxBytes per stream file', 'TTL from first append; 0 keeps']} />
                <Facts title="Rotation on a new stream" items={['remove expired streams', 'then least recently written', 'beyond KeepStreams', 'busy streams skipped (TryLock)']} />
              </div>
            </BoxNode>
          </div>

          <Arrow variant="primary" from={id('append')} to={id('backend')} startAnchor="right"
            endAnchor={{ position: 'left', offset: { y: -110 } }} labels={{ middle: <FlowLabel text="Append" /> }} />
          <Arrow variant="secondary" from={id('scan')} to={id('backend')} startAnchor="right"
            endAnchor={{ position: 'left', offset: { y: 110 } }} labels={{ middle: <FlowLabel text="Scan(afterSeq)" /> }} />
          <Arrow variant="primary" from={id('backend')} to={id('dir')} path="straight" startAnchor="right" endAnchor="left"
            showTail tailShape="arrow1" />
        </>
      )}
    </Diagram>
  );
}

export function NDJSONStorageLayout() {
  return (
    <Diagram className="relative py-6">
      {(id) => (
        <>
          <div className="grid justify-center items-center justify-items-center gap-x-28 gap-y-28"
            style={{ gridTemplateColumns: 'repeat(3, max-content)' }}>
            <div className="row-span-2">
              <BoxNode id={id('sidecar')} title={<HeaderTitle icon={HiCodeBracket}>{'<stream>.meta.json'}</HeaderTitle>}
                headerColor={COLORS.primary} bodyColor={COLORS.background} compact minWidth="220px"
                ports={[{ position: 'bottom-middle', icon: HiArrowPath, label: 'written as .tmp, renamed over', color: COLORS.muted }]}>
                <div className="flex flex-col">
                  {[
                    { name: 'stream', type: 'string', pk: true },
                    { name: 'kind · generation', type: 'string · uuid' },
                    { name: 'total', type: 'int' },
                    { name: 'lowSeq · highSeq', type: 'int' },
                    { name: 'updatedAt · expiresAt', type: 'time' },
                    { name: 'capped', type: 'bool' },
                    { name: 'file', type: 'data file name', fk: true },
                    { name: 'bytes', type: 'committed length' },
                    { name: 'appends', type: '[{last, at}]' },
                    { name: 'keys', type: '{key: seq}' },
                  ].map((field) => <FieldRow key={field.name} {...field} />)}
                </div>
              </BoxNode>
            </div>
            <EntityBox id={id('data')} title="<stream>.ndjson" accent={COLORS.fk} minWidth="210px" fields={[
              { name: 'lines', type: 'seq lowSeq … highSeq' },
              { name: 'size', type: '≤ MaxBytes' },
              { name: 'past bytes', type: 'torn append, cut next write' },
            ]} note="append-only; the sidecar's bytes is where committed data ends" />
            <EntityBox id={id('line')} title="line" accent={COLORS.accent} minWidth="200px" fields={[
              { name: 'seq', type: 'int, written first', pk: true },
              { name: 'row', type: 'JSON object' },
            ]} note={'{"seq":N,"row":{…}}\\n; bisect reads the {"seq": prefix'} />
            <EntityBox id={id('trimmed')} title="<stream>@<low>.ndjson" accent={COLORS.fk} minWidth="210px" fields={[
              { name: 'lines', type: 'seq low … highSeq' },
              { name: 'written', type: 'before the sidecar names it' },
              { name: 'old file', type: 'removed after commit' },
            ]} note="@ is outside the stream id alphabet, so no other stream owns it" />
            <div />
          </div>

          <Arrow variant="er" from={id('sidecar')} to={id('data')} color={COLORS.fk}
            startAnchor={{ position: 'right', offset: { y: -70 } }} endAnchor="left"
            labels={{ middle: <RelLabel text="file · 1:1" /> }} />
          <Arrow variant="er" from={id('sidecar')} to={id('trimmed')} color={COLORS.fk} dashness
            startAnchor={{ position: 'right', offset: { y: 70 } }} endAnchor="left"
            labels={{ middle: <RelLabel text="file, after Trim" /> }} />
          <Arrow variant="secondary" from={id('data')} to={id('trimmed')} path="straight" startAnchor="bottom" endAnchor="top"
            labels={{ middle: <FlowLabel text="Trim copies seq ≥ cut" /> }} />
          <Arrow variant="er" from={id('data')} to={id('line')} path="straight" startAnchor="right" endAnchor="left"
            labels={{ middle: <RelLabel text="1:N" /> }} />
        </>
      )}
    </Diagram>
  );
}

