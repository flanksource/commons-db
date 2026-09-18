// Example: a SQL Server deadlock sync — xml_deadlock_report graphs read
// from system_health's event_file — stored in records.sqlite or valkey.
import React from 'react';
import { SqlServer } from '@flanksource/icons/mi';
import { HiLockClosed } from 'react-icons/hi2';
import { Facts } from './parts';
import { CaptureStoreDiagram, StoreComparison, SampleRows, KeyList, Path } from './capture';

const stream = 'sql-deadlocks.uat';
const id = '20260910T164750043Z-processe80b448c8';

export function SQLDeadlocksCapture() {
  return (
    <CaptureStoreDiagram
      producer={{
        title: 'deadlocks.Sync', icon: HiLockClosed,
        source: <><SqlServer className="w-4 h-4" /> system_health · event_file</>,
        steps: [
          { label: 'newestStored', detail: 'scan the last 100 rows for the newest timestamp' },
          { label: 'Load', detail: 'xml_deadlock_report from newest − dispatch latency − 2s' },
          { label: 'RowFromGraph', detail: 'decode, resolve hobts, analyze the shape' },
          { label: 'AppendTyped', detail: 'sorted by (timestamp, id); stored ids are skipped' },
        ],
        note: <Facts title="Runs on" items={['sql deadlocks sync · list · get', 'a span re-reads [from, now) per sample']} />,
      }}
      contract={{
        kind: 'sql_deadlock',
        declared: [
          { name: 'stream', type: 'sql-deadlocks.<env>' },
          { name: 'KeyColumn', type: 'id', pk: true },
          { name: 'Retention', type: 'RetainRows' },
          { name: 'TimeColumn', type: 'timestamp' },
          { name: 'DefaultFrom', type: 'now-12h' },
        ],
        columns: [
          { name: 'id', type: 'ts(ms)-lead process', pk: true },
          { name: 'timestamp', type: 'datetime' },
          { name: 'shape · signature', type: 'string' },
          { name: 'victimApp · statements', type: 'string' },
          { name: 'processes · resources', type: 'json' },
          { name: 'xml', type: '12–64 KB' },
        ],
      }}
      sqlite={
        <>
          <Path>{'…/environments/uat/records.sqlite'}</Path>
          <SampleRows table="records_sql_deadlock" columns={['stream_id', 'seq', 'id']} rows={[
            [stream, '12', '…T091502117Z-process3f1c…'],
            [stream, '41', '…T164750043Z-processe80b…'],
          ]} />
          <Facts title="Its own index" items={['UNIQUE (stream_id, id): EXISTS skips a re-read graph', 'each append deletes rows older than 30d']} />
        </>
      }
      kv={
        <KeyList prefix={`<keyPrefix>trace-results/${stream}/`} keys={[
          { key: 'meta', type: 'STRING', value: 'lowSeq 12 · highSeq 41 · expires now+30d' },
          { key: 'seqs', type: 'ZSET', value: `…0041/${id.slice(0, 19)}… → 41` },
          { key: 'appended', type: 'ZSET', value: 'same members → append µs' },
          { key: `row/${id}`, type: 'STRING', value: '{generation, seq 41, row {…, xml}}' },
        ]} />
      }
      index={['copies seq > indexed high', 'TrimBelow mirrors aged-out rows', 'SetExpiry mirrors the slide']}
      reader={{ profile: 'trace-results/sql_deadlock', items: ['sql deadlocks list --from now-12h', 'get <id>: ±1s around its timestamp', 'web /sql/deadlocks'] }}
    />
  );
}

export function SQLDeadlocksComparison() {
  return (
    <StoreComparison title="sql_deadlock in each backend" rows={[
      { aspect: 'Chosen when', sqlite: 'the environment cache has no valkey L2: a CLI run with no redis.url', kv: 'the environment cache has a valkey L2: serve, or a CLI run with redis.url' },
      { aspect: 'Overlapping re-read', sqlite: 'EXISTS (stream_id, id) per graph inside the append transaction, backed by the UNIQUE index', kv: 'one MGET of row/<id> for the batch before anything is written; a row counts only under meta.generation within lowSeq … highSeq' },
      { aspect: 'Concurrent syncs', sqlite: 'within one process the stream lock and single writer serialize them, and the second skips what the first stored; across processes each append is one SQLite transaction', kv: 'serialized within one process by StreamLocks; two processes appending one stream are outside the recordstore contract' },
      { aspect: 'Retention', sqlite: 'RetainRows: each append deletes rows appended over 30d ago and slides expires_at; an idle stream is swept', kv: 'RetainRows: trim moves lowSeq and drops set members; meta and sets slide to now+30d; row keys age out on their own TTL' },
      { aspect: 'Row size', sqlite: 'about 2–3× the report XML: raw xml plus decoded processes and resources, in typed columns', kv: 'the same payload per row/<id> key, under the 4MiB cap' },
      { aspect: 'Profile read', sqlite: 'reads records.sqlite directly; from/to over timestamp', kv: 'Indexer.Ensure copies new seqs and mirrors trim and expiry into index.sqlite first' },
    ]} />
  );
}
