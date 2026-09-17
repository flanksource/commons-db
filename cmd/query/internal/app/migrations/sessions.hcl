table "sessions" {
  schema = schema.public

  column "id" {
    null = false
    type = text
  }
  column "profile_name" {
    null = false
    type = text
  }
  column "kind" {
    null = false
    type = text
  }
  column "role" {
    null    = false
    type    = text
    default = "capture"
  }
  column "principal" {
    null = true
    type = text
  }
  column "restart_of" {
    null = true
    type = text
  }
  // params predates start, which carries the params; only legacy rows set it.
  column "params" {
    null = true
    type = jsonb
  }
  // start is the immutable query.SessionStart JSON, status the query.SessionStatus
  // JSON the owner overwrites; the scalar columns mirror them for SQL filters.
  column "start" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "status" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "state" {
    null = false
    type = text
  }
  column "error" {
    null = true
    type = text
  }
  column "event_count" {
    null    = false
    type    = bigint
    default = 0
  }
  column "started_at" {
    null = false
    type = timestamptz
  }
  column "stopped_at" {
    null = true
    type = timestamptz
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }
  column "updated_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }

  primary_key {
    columns = [column.id]
  }

  index "sessions_profile_name_idx" {
    columns = [column.profile_name]
  }
  index "sessions_state_idx" {
    columns = [column.state]
  }
  index "sessions_role_idx" {
    columns = [column.role]
  }
  index "sessions_principal_idx" {
    columns = [column.principal]
  }
  index "sessions_restart_of_idx" {
    columns = [column.restart_of]
  }
  index "sessions_started_at_idx" {
    columns = [column.started_at]
  }
  index "sessions_stopped_at_idx" {
    columns = [column.stopped_at]
  }
  index "sessions_updated_at_idx" {
    columns = [column.updated_at]
  }
}

table "session_events" {
  schema = schema.public

  column "session_id" {
    null = false
    type = text
  }
  column "sequence" {
    null = false
    type = bigint
  }
  column "time" {
    null = false
    type = timestamptz
  }
  column "payload" {
    null = false
    type = jsonb
  }

  primary_key {
    columns = [column.session_id, column.sequence]
  }

  foreign_key "session_events_session_id_fkey" {
    columns     = [column.session_id]
    ref_columns = [table.sessions.column.id]
    on_delete   = CASCADE
  }
}
