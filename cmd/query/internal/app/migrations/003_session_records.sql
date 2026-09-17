-- Sessions recorded before the start and status columns existed carry the
-- column defaults ('{}'). Rebuild both from the legacy scalar columns, so every
-- row reads back as a query.SessionRecord; the store refuses an empty start.
UPDATE sessions
SET start = jsonb_strip_nulls(jsonb_build_object(
      'schemaVersion', 1,
      'id', id,
      'profile', profile_name,
      'kind', kind,
      'role', role,
      'params', params,
      'owner', jsonb_build_object('host', '', 'pid', 0, 'boot', ''),
      'startedAt', started_at
    )),
    status = jsonb_strip_nulls(jsonb_build_object(
      'state', state,
      'error', NULLIF(error, ''),
      'eventCount', event_count,
      'updatedAt', updated_at,
      'heartbeatAt', updated_at,
      'stoppedAt', stopped_at
    ))
WHERE start = '{}'::jsonb;
