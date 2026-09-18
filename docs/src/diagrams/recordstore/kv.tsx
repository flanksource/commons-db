// recordstore/kv: record streams as keys in a clicky cache.Store (in-process
// memory or valkey/redis). An unkeyed kind is stored in chunks, a keyed kind one
// key per row; both commit through the stream's meta key.
import React from 'react';
import { Diagram, BoxNode, Arrow, NodeSection, COLORS } from '@flanksource/facet';
import { Redis, Valkey } from '@flanksource/icons/mi';
import { HiBell, HiMagnifyingGlass, HiLockClosed, HiQueueList, HiCircleStack } from 'react-icons/hi2';
import { HeaderTitle, IconNode, StepList, Facts, EntityBox, RelLabel, FlowLabel } from './parts';

export function KVAppendFlow() {
  return (
    <Diagram className="relative py-6">
      {(id) => (
        <>
          <div className="flex items-center justify-center gap-5">
            <div className="flex flex-col gap-24">
              <BoxNode id={id('append')} title={<HeaderTitle icon={HiBell}>Notifier.Append</HeaderTitle>}
                headerColor={COLORS.primary} bodyColor={COLORS.background} compact minWidth="190px">
                <Facts title="Writer" items={['one process per stream', 'wakes followers on commit']} />
              </BoxNode>
              <BoxNode id={id('scan')} title={<HeaderTitle icon={HiMagnifyingGlass}>Scan / Tail</HeaderTitle>}
                headerColor={COLORS.muted} bodyColor={COLORS.background} borderColor={COLORS.muted} compact minWidth="190px">
                <Facts title="Reader" items={['server, Indexer.Ensure', 'resumes after a seq']} />
              </BoxNode>
            </div>

            <div className="flex flex-col items-center gap-2" style={{ minWidth: '150px' }}>
              <IconNode id={id('locks')} icon={HiLockClosed} label="StreamLocks" caption="serialize appends" />
              <IconNode id={id('schema')} icon={HiQueueList} label="SchemaResolver" caption="key → layout, retention" />
            </div>

            <BoxNode id={id('backend')} title={<HeaderTitle icon={HiCircleStack}>kv.Backend</HeaderTitle>}
              headerColor={COLORS.primary} bodyColor={COLORS.background} minWidth="280px">
              <div className="flex flex-col gap-2">
                <StepList title="Append, under the stream lock" steps={[
                  { label: 'openStream', detail: 'GET meta, or a new generation expiring now+TTL' },
                  { label: 'prune', detail: 'drop entries past HighSeq a crash left' },
                  { label: 'trim', detail: 'RetainRows: LowSeq past rows older than ttl' },
                  { label: 'unstored', detail: 'keyed: MGET row/<key>, skip committed keys' },
                  { label: 'write', detail: 'chunks or rows, ahead of the meta' },
                  { label: 'slide', detail: 'RetainRows: move expiry to now+ttl' },
                  { label: 'writeMeta', detail: 'SET meta with the stream ttl, the commit point', commit: true },
                ]} />
                <Facts title="Scan" items={['reads only LowSeq … HighSeq of meta', 'a missing seq is an error, not a short stream']} />
              </div>
            </BoxNode>

            <div style={{ minWidth: '70px' }} />

            <BoxNode id={id('store')} title="cache.Store"
              headerColor={COLORS.accent} bodyColor={COLORS.background} borderColor={COLORS.accent} minWidth="220px">
              <div className="flex flex-col gap-2">
                <div className="flex flex-col gap-1">
                  <div className="text-[9px] font-bold uppercase tracking-wide" style={{ color: COLORS.muted }}>Implementations</div>
                  <div className="flex items-center gap-2 text-[10px]" style={{ color: COLORS.accent }}>
                    <HiCircleStack className="w-4 h-4" /> cache.NewMemory · in process
                  </div>
                  <div className="flex items-center gap-2 text-[10px]" style={{ color: COLORS.accent }}>
                    <Valkey className="w-4 h-4" /><Redis className="w-4 h-4" /> valkey.NewStore · shared
                  </div>
                </div>
                <NodeSection title="Strings" items={['GET · SET PX · DEL · PEXPIRE', 'MGET row/<key> …']} />
                <NodeSection title="Sorted sets" items={['ZADD · ZREM', 'ZRANGEBYSCORE', 'ZREMRANGEBYSCORE']} />
              </div>
            </BoxNode>
          </div>

          <Arrow variant="primary" from={id('append')} to={id('backend')} startAnchor="right"
            endAnchor={{ position: 'left', offset: { y: -110 } }} labels={{ middle: <FlowLabel text="Append" /> }} />
          <Arrow variant="secondary" from={id('scan')} to={id('backend')} startAnchor="right"
            endAnchor={{ position: 'left', offset: { y: 110 } }} labels={{ middle: <FlowLabel text="Scan(afterSeq)" /> }} />
          <Arrow variant="primary" from={id('backend')} to={id('store')} path="straight" startAnchor="right" endAnchor="left"
            showTail tailShape="arrow1" />
        </>
      )}
    </Diagram>
  );
}

const metaFields = [
  { name: 'stream', type: 'string', pk: true },
  { name: 'kind', type: 'string' },
  { name: 'generation', type: 'uuid' },
  { name: 'total', type: 'int' },
  { name: 'lowSeq · highSeq', type: 'int' },
  { name: 'updatedAt · expiresAt', type: 'time' },
  { name: 'capped', type: 'bool' },
];

export function KVChunkLayout() {
  return (
    <Diagram className="relative py-6">
      {(id) => (
        <>
          <div className="grid justify-center items-center justify-items-center gap-x-32 gap-y-10"
            style={{ gridTemplateColumns: 'repeat(3, max-content)' }}>
            <EntityBox id={id('meta')} title="meta · STRING" fields={metaFields}
              note="JSON recordstore.Meta; readers trust nothing outside lowSeq … highSeq" />
            <EntityBox id={id('index')} title="index · ZSET" accent={COLORS.accent} fields={[
              { name: 'member', type: '<first seq, 19 digits>', pk: true },
              { name: 'score', type: 'first seq' },
            ]} note="scan order: chunks by the seq of their first row" />
            <EntityBox id={id('chunk')} title="chunk/<first> · STRING" accent={COLORS.fk} fields={[
              { name: 'value', type: '[row, row, …]' },
              { name: 'size', type: '≤ MaxChunkBytes' },
            ]} note="one append's rows, split to the cap; written once, never rewritten" />
            <div />
            <div />
            <EntityBox id={id('appended')} title="appended · ZSET" accent={COLORS.accent} fields={[
              { name: 'member', type: '<first seq, 19 digits>', pk: true },
              { name: 'score', type: 'append time µs' },
            ]} note="Trim: chunks appended before an instant" />
          </div>

          <Arrow variant="er" from={id('meta')} to={id('index')} path="straight" startAnchor="right" endAnchor="left"
            labels={{ middle: <RelLabel text="bounds seqs" /> }} />
          <Arrow variant="er" from={id('index')} to={id('chunk')} path="straight" startAnchor="right" endAnchor="left" color={COLORS.fk}
            labels={{ middle: <RelLabel text="1:1" /> }} />
          <Arrow variant="er" from={id('appended')} to={id('chunk')} path="straight" startAnchor="top" endAnchor="bottom" color={COLORS.fk}
            labels={{ middle: <RelLabel text="1:1" /> }} />
        </>
      )}
    </Diagram>
  );
}

export function KVRowLayout() {
  return (
    <Diagram className="relative py-6">
      {(id) => (
        <>
          <div className="grid justify-center items-center justify-items-center gap-x-32 gap-y-10"
            style={{ gridTemplateColumns: 'repeat(3, max-content)' }}>
            <EntityBox id={id('meta')} title="meta · STRING" fields={metaFields}
              note="a row counts only under this generation, between lowSeq and highSeq" />
            <EntityBox id={id('seqs')} title="seqs · ZSET" accent={COLORS.accent} fields={[
              { name: 'member', type: '<seq>/<key>', pk: true },
              { name: 'score', type: 'seq' },
            ]} note="scan order; trim drops members below lowSeq" />
            <EntityBox id={id('row')} title="row/<key> · STRING" accent={COLORS.fk} fields={[
              { name: 'generation', type: 'uuid' },
              { name: 'seq', type: 'int' },
              { name: 'row', type: 'JSON' },
              { name: 'ttl', type: 'stream ttl + retention' },
            ]} note="an append reads it to skip a stored key; no append moves its expiry, so trimmed rows age out" />
            <div />
            <div />
            <EntityBox id={id('appended')} title="appended · ZSET" accent={COLORS.accent} fields={[
              { name: 'member', type: '<seq>/<key>', pk: true },
              { name: 'score', type: 'append time µs' },
            ]} note="Trim: rows appended before an instant" />
          </div>

          <Arrow variant="er" from={id('meta')} to={id('seqs')} path="straight" startAnchor="right" endAnchor="left"
            labels={{ middle: <RelLabel text="bounds seqs" /> }} />
          <Arrow variant="er" from={id('seqs')} to={id('row')} path="straight" startAnchor="right" endAnchor="left" color={COLORS.fk}
            labels={{ middle: <RelLabel text="1:1 by key" /> }} />
          <Arrow variant="er" from={id('appended')} to={id('row')} path="straight" startAnchor="top" endAnchor="bottom" color={COLORS.fk}
            labels={{ middle: <RelLabel text="1:1 by key" /> }} />
        </>
      )}
    </Diagram>
  );
}

