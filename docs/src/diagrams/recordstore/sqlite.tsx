// recordstore/sqlite: one SQLite file holding a table per kind plus the stream
// catalog. The same file is the index a `sql` profile pages through ReadDSN.
import React from 'react';
import { Diagram, BoxNode, Arrow, COLORS } from '@flanksource/facet';
import { SiSqlite } from 'react-icons/si';
import { HiBell, HiArrowsRightLeft, HiMagnifyingGlass, HiTableCells, HiClock } from 'react-icons/hi2';
import { HeaderTitle, StepList, Facts, EntityBox, RelLabel, FlowLabel } from './parts';

export function SQLiteAppendFlow() {
  return (
    <Diagram className="relative py-6">
      {(id) => (
        <>
          <div className="flex items-center justify-center gap-5">
            <div className="flex flex-col gap-10">
              <BoxNode id={id('append')} title={<HeaderTitle icon={HiBell}>Notifier.Append</HeaderTitle>}
                headerColor={COLORS.primary} bodyColor={COLORS.background} compact minWidth="200px">
                <Facts title="Durable store" items={['records.sqlite, its own index', 'Derived=false · TTL from settings']} />
              </BoxNode>
              <BoxNode id={id('import')} title={<HeaderTitle icon={HiArrowsRightLeft}>Indexer.Import</HeaderTitle>}
                headerColor={COLORS.muted} bodyColor={COLORS.background} borderColor={COLORS.muted} compact minWidth="200px">
                <Facts title="Derived index" items={[
                  'index.sqlite · Derived=true · TTL 0',
                  'Prepare drops an older generation',
                  'First must be HighSeq+1',
                  'TrimBelow / SetExpiry mirror source',
                ]} />
              </BoxNode>
            </div>

            <div style={{ minWidth: '30px' }} />

            <BoxNode id={id('backend')} title={<HeaderTitle icon={SiSqlite}>sqlite.Backend</HeaderTitle>}
              headerColor={COLORS.primary} bodyColor={COLORS.background} minWidth="270px">
              <div className="flex flex-col gap-2">
                <StepList title="Before the transaction" steps={[
                  { label: 'purgeExpired', detail: 'drop an expired incarnation' },
                  { label: 'kindTable', detail: 'reconcile records_<kind> by storage signature' },
                  { label: 'planAppend', detail: 'typed rows, keys, retention' },
                ]} />
                <StepList title="One write transaction" start={4} steps={[
                  { label: 'openStream', detail: 'record_streams row or new generation' },
                  { label: 'trimTx', detail: 'RetainRows: drop rows older than ttl' },
                  { label: 'unstoredRows', detail: 'EXISTS (stream_id = ? AND key = ?)' },
                  { label: 'insertRows', detail: 'seqs HighSeq+1… + record_appends' },
                  { label: 'upsertMeta → COMMIT', detail: 'commit point: rows + meta together', commit: true },
                ]} />
              </div>
            </BoxNode>

            <div style={{ minWidth: '80px' }} />

            <BoxNode id={id('db')} title={<HeaderTitle icon={HiTableCells}>sqlitedb.DB</HeaderTitle>}
              headerColor={COLORS.accent} bodyColor={COLORS.background} borderColor={COLORS.accent} minWidth="210px"
              ports={[{ position: 'bottom-middle', icon: HiClock, label: 'Sweeper · every SweepInterval', color: COLORS.muted }]}>
              <div className="flex flex-col gap-2">
                <Facts title="Writer" items={['1 connection · WAL', 'busy_timeout 5s', 'Write(fn) serializes mutations']} />
                <Facts title="Readers" items={['pool of 10 connections', 'Lease() holds off mutations', 'while a reader pages']} />
                <Facts title="Sweep" items={['DELETE streams past expires_at']} />
              </div>
            </BoxNode>

            <div style={{ minWidth: '30px' }} />

            <div className="flex flex-col gap-10">
              <BoxNode id={id('scan')} title={<HeaderTitle icon={HiMagnifyingGlass}>Backend.Scan</HeaderTitle>}
                headerColor={COLORS.primary} bodyColor={COLORS.background} compact minWidth="210px">
                <Facts title="Read-only snapshot" items={['WHERE stream_id = ? AND seq > ?', 'ORDER BY seq LIMIT 1000', 'page by page in one tx']} />
              </BoxNode>
              <BoxNode id={id('profile')} title="sql profile"
                headerColor={COLORS.outputBorder} bodyColor={COLORS.background} borderColor={COLORS.outputBorder} compact minWidth="210px">
                <Facts title="ReadDSN" items={['mode=ro · query_only(1)', 'page, filter, export', 'records_<kind> by column']} />
              </BoxNode>
            </div>
          </div>

          <Arrow variant="primary" from={id('append')} to={id('backend')} startAnchor="right"
            endAnchor={{ position: 'left', offset: { y: -70 } }} />
          <Arrow variant="secondary" from={id('import')} to={id('backend')} startAnchor="right"
            endAnchor={{ position: 'left', offset: { y: 70 } }} />
          <Arrow variant="primary" from={id('backend')} to={id('db')} path="straight" startAnchor="right" endAnchor="left"
            labels={{ middle: <FlowLabel text="Write(tx)" /> }} />
          <Arrow variant="primary" from={id('db')} to={id('scan')} startAnchor={{ position: 'right', offset: { y: -40 } }} endAnchor="left" />
          <Arrow variant="secondary" from={id('db')} to={id('profile')} startAnchor={{ position: 'right', offset: { y: 40 } }} endAnchor="left" />
        </>
      )}
    </Diagram>
  );
}

export function SQLiteStorageLayout() {
  return (
    <Diagram className="relative py-6">
      {(id) => (
        <>
          <div className="flex justify-center items-start gap-16 mb-14">
            <div style={{ minWidth: '230px' }} />
            <EntityBox id={id('kinds')} title="record_kinds" accent={COLORS.accent} fields={[
              { name: 'kind', type: 'TEXT', pk: true },
              { name: 'table_name', type: 'TEXT' },
              { name: 'columns', type: 'TEXT' },
            ]} note="columns is the storage signature: a change drops a derived table, fails a durable one" />
            <EntityBox id={id('format')} title="record_store_format" accent={COLORS.muted} fields={[
              { name: 'key', type: 'INTEGER = 1', pk: true },
              { name: 'version', type: 'INTEGER = 2' },
            ]} note="catalog version; a mismatch rebuilds a derived file" />
          </div>

          <div className="flex justify-center items-start gap-16">
            <EntityBox id={id('records')} title="records_<kind>" accent={COLORS.fk} minWidth="230px" fields={[
              { name: 'stream_id', type: 'TEXT', pk: true, fk: true },
              { name: 'seq', type: 'INTEGER', pk: true },
              { name: '<kind columns>', type: 'derived safe names' },
              { name: '(stream_id, key column)', type: 'keyed kinds', index: 'UQ' },
            ]} note="columns stored positionally, aliased to declared names on SELECT" />
            <EntityBox id={id('streams')} title="record_streams" fields={[
              { name: 'stream_id', type: 'TEXT', pk: true },
              { name: 'generation', type: 'TEXT' },
              { name: 'kind', type: 'TEXT', fk: true },
              { name: 'total', type: 'INTEGER' },
              { name: 'low_seq', type: 'INTEGER' },
              { name: 'high_seq', type: 'INTEGER' },
              { name: 'updated_at', type: 'TEXT' },
              { name: 'expires_at', type: 'TEXT', index: 'IDX' },
              { name: 'capped', type: 'INTEGER' },
            ]} />
            <EntityBox id={id('appends')} title="record_appends" accent={COLORS.fk} fields={[
              { name: 'stream_id', type: 'TEXT', pk: true, fk: true },
              { name: 'last_seq', type: 'INTEGER', pk: true },
              { name: 'appended_at', type: 'TEXT' },
            ]} note="one row per append: Trim finds the last seq appended before an instant" />
          </div>

          <Arrow variant="er" from={id('records')} to={id('streams')} path="straight" color={COLORS.fk}
            startAnchor={{ position: 'right', offset: { y: -30 } }} endAnchor={{ position: 'left', offset: { y: -60 } }}
            labels={{ middle: <RelLabel text="N:1" /> }} />
          <Arrow variant="er" from={id('appends')} to={id('streams')} path="straight" color={COLORS.fk}
            startAnchor={{ position: 'left', offset: { y: -20 } }} endAnchor={{ position: 'right', offset: { y: -50 } }}
            labels={{ middle: <RelLabel text="N:1" /> }} />
          <Arrow variant="er" from={id('streams')} to={id('kinds')} path="straight" color={COLORS.fk}
            startAnchor="top" endAnchor="bottom"
            labels={{ middle: <RelLabel text="N:1" /> }} />
          <Arrow variant="er" from={id('kinds')} to={id('records')} dashness
            startAnchor="left" endAnchor="top"
            labels={{ middle: <RelLabel text="table_name names it" /> }} />
        </>
      )}
    </Diagram>
  );
}

