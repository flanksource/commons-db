// Example: a sql_xevent trace span — SQL Server Extended Events polled
// from a ring_buffer — stored in records.sqlite or valkey.
import React from 'react';
import { SqlServer } from '@flanksource/icons/mi';
import { HiCircleStack } from 'react-icons/hi2';
import { Facts } from './parts';
import { CaptureStoreDiagram, StoreComparison, SampleRows, KeyList, Path } from './capture';

const stream = '9f2c41d07ab35e18';
const short = '9f2c…5e18';

export function SQLXEventsCapture() {
  return (
    <CaptureStoreDiagram
      producer={{
        title: 'sql_xevent span', icon: HiCircleStack,
        source: <><SqlServer className="w-4 h-4" /> XE session · ring_buffer</>,
        steps: [
          { label: 'Start', detail: 'XE session + drain; empty append creates stream trace.ID' },
          { label: 'drain every 1s', detail: 're-reads the whole ring; Drain dedupes on Event.Key into the cache EventStore' },
          { label: 'Sample, per step', detail: 'EventsSince(lastKey) → FromXEvent → append' },
          { label: 'Stop', detail: 'wait dispatch latency, final drain, append the rest' },
        ],
      }}
      contract={{
        kind: 'sql_xevent',
        declared: [
          { name: 'stream', type: 'trace.ID, 16 hex' },
          { name: 'KeyColumn', type: 'none: unkeyed' },
          { name: 'Retention', type: 'RetainStream' },
          { name: 'TimeColumn', type: 'timestamp' },
        ],
        columns: [
          { name: 'name · statementType', type: 'string' },
          { name: 'timestamp', type: 'datetime' },
          { name: 'durationMs · cpuMs', type: 'number' },
          { name: 'sql · tables', type: 'string · json' },
          { name: 'children', type: 'json, nested' },
        ],
      }}
      sqlite={
        <>
          <Path>{'…/environments/<env>/records.sqlite'}</Path>
          <SampleRows table="records_sql_xevent" columns={['stream_id', 'seq', 'name', 'durationMs']} rows={[
            [short, '1', 'rpc_completed', '12.4'],
            [short, '2', 'sql_batch_completed', '0.8'],
            [short, '1840', 'rpc_completed', '231.0'],
          ]} />
          <Facts title="Its own index" items={['one tx per Sample: rows, record_appends, record_streams', 'no UNIQUE index: every row is stored']} />
        </>
      }
      kv={
        <KeyList prefix={`<keyPrefix>trace-results/${stream}/`} keys={[
          { key: 'meta', type: 'STRING', value: 'sql_xevent · lowSeq 1 · highSeq 1840' },
          { key: 'index', type: 'ZSET', value: '…0001 → 1 · …0613 → 613' },
          { key: 'chunk/0000000000000000001', type: 'STRING', value: '[row × 612] ≤ 4MiB' },
          { key: 'chunk/0000000000000000613', type: 'STRING', value: '[row × 1228]' },
          { key: 'appended', type: 'ZSET', value: 'chunk → append µs' },
        ]} />
      }
      index={['Indexer.Ensure before a read', 'copies seq > indexed high', '500 rows per import']}
      reader={{ profile: 'trace-results/sql_xevent', items: ['web pins a Ref window:', 'afterSeq=from-1&toSeq=to', 'newest first by timestamp'] }}
    />
  );
}

export function SQLXEventsComparison() {
  return (
    <StoreComparison title="sql_xevent in each backend" rows={[
      { aspect: 'Chosen when', sqlite: 'the environment cache has no valkey L2: a CLI run with no redis.url', kv: 'the environment cache has a valkey L2: serve, or a CLI run with redis.url' },
      { aspect: 'One Sample append', sqlite: 'one write transaction on the single writer connection: rows, record_appends and record_streams commit together', kv: 'SET chunk/<first> (split at 4MiB), ZADD index and appended, then SET meta as the commit point' },
      { aspect: 'Repeated events', sqlite: 'stored again: the kind is unkeyed', kv: 'stored again: the kind is unkeyed' },
      { aspect: 'Largest row', sqlite: 'no cap: a row with many children is one wide record', kv: 'kvChunkBytes 4MiB: a larger row refuses the whole append and marks the stream capped' },
      { aspect: 'Expiry', sqlite: 'the whole stream at expires_at, 30d after its first append; the sweeper deletes it within 10m', kv: 'the whole stream: meta, index, appended and every chunk carry its 30d expiry, so it leaves valkey in one piece' },
      { aspect: 'Profile read', sqlite: 'reads records.sqlite, which is its own index', kv: 'Indexer.Ensure copies the new seqs into index.sqlite, then the profile reads that' },
    ]} />
  );
}
