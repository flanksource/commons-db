// Example: JVM probe watches and traces — a diagnostics agent's bytecode probes
// read from the agent's ring — stored in records.sqlite or valkey.
import React from 'react';
import { Java } from '@flanksource/icons/mi';
import { FaJava } from 'react-icons/fa';
import { Facts } from './parts';
import { CaptureStoreDiagram, StoreComparison, SampleRows, KeyList, Path } from './capture';

const stream = 'jvm-probe:app:ActivityProcessor.process.9f3a1c2e:2';

export function JVMProbesCapture() {
  return (
    <CaptureStoreDiagram
      producer={{
        title: 'ProbeFollower', icon: FaJava,
        source: <><Java className="w-4 h-4" /> diagnostics agent ring</>,
        steps: [
          { label: 'Follow', detail: 'serve: from OldestSeq, replaying the ring; span: a fresh probe' },
          { label: 'read', detail: 'GET classes/probes?since until NextSeq ≥ WriteSeq, generation pinned' },
          { label: 'probeevent.Stream', detail: 'one final row per call when it returns or throws' },
          { label: 'Close', detail: 'a span stores calls still open as status open' },
        ],
        note: <Facts title="watch vs trace" items={['calls:false: the watched method', 'calls:true: its callee tree too, ~10× rows']} />,
      }}
      contract={{
        kind: 'jvm_trace',
        declared: [
          { name: 'stream', type: 'jvm-probe:<target>:<key>:<gen>' },
          { name: 'KeyColumn', type: 'id', pk: true },
          { name: 'Retention', type: 'RetainStream' },
          { name: 'TimeColumn', type: 'none: seq order' },
        ],
        columns: [
          { name: 'id', type: 'f<thread>-<frame>', pk: true },
          { name: 'rootId · parentId', type: 'string' },
          { name: 'class · method', type: 'string' },
          { name: 'durationUs · status', type: 'number · string' },
          { name: 'args · return', type: 'json graphs' },
        ],
      }}
      sqlite={
        <>
          <Path>{'…/environments/<env>/records.sqlite'}</Path>
          <SampleRows table="records_jvm_trace" columns={['stream_id', 'seq', 'id', 'rootId', 'status']} rows={[
            ['jvm-probe:app:…:2', '1', 'f41-8', 'f41-7', 'ok'],
            ['jvm-probe:app:…:2', '2', 'f41-7', 'f41-7', 'ok'],
            ['jvm-probe:app:…:2', '3', 'f52-3', 'f52-3', 'error'],
          ]} />
          <Facts title="Its own index" items={['UNIQUE (stream_id, id): a replayed call is skipped', 'expires 30d after the first append']} />
        </>
      }
      kv={
        <KeyList prefix={`<keyPrefix>trace-results/${stream}/`} keys={[
          { key: 'meta', type: 'STRING', value: 'jvm_trace · lowSeq 1 · highSeq 3' },
          { key: 'seqs', type: 'ZSET', value: '…0001/f41-8 → 1 · …0002/f41-7 → 2' },
          { key: 'appended', type: 'ZSET', value: 'same members → append µs' },
          { key: 'row/f41-7', type: 'STRING', value: '{generation, seq 2, row {args, return}}' },
        ]} />
      }
      index={['copies seq > indexed high', 'one probe generation per stream', 'tree read folds rootId']}
      reader={{ profile: 'trace-results/jvm_trace', items: ['trace-results jvm-tree --stream --id', 'probes/keys: each key’s Ref', 'and follower status'] }}
    />
  );
}

export function JVMProbesComparison() {
  return (
    <StoreComparison title="jvm_trace in each backend" rows={[
      { aspect: 'Chosen when', sqlite: 'the environment cache has no valkey L2: a CLI trace span with no redis.url', kv: 'the environment cache has a valkey L2: serve and its followers, or a CLI run with redis.url' },
      { aspect: 'Follower restart', sqlite: 'the deterministic stream id reopens the same stream; EXISTS (stream_id, id) skips every replayed call', kv: 'the same stream id; one MGET of row/f<thread>-<frame> skips every replayed call' },
      { aspect: 'Open calls', sqlite: 'the first row stored for an id wins, so an open row stored early would hide the real return', kv: 'the same rule, which is why serve stopping stores no open calls' },
      { aspect: 'Row size', sqlite: 'args and return graphs up to 2,000 nodes and 262K chars per value, no cap', kv: 'the same payload per row/<id> key; a row over 4MiB refuses the append and caps the stream' },
      { aspect: 'Expiry', sqlite: 'the whole stream at expires_at, 30d after its first append; swept every 10m', kv: 'the whole stream: meta and sets carry the 30d expiry, row keys expire with it' },
      { aspect: 'Profile read', sqlite: 'reads records.sqlite directly, rows in seq order', kv: 'Indexer.Ensure copies new seqs into index.sqlite before the read' },
    ]} />
  );
}
