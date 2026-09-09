schema "public" {
}

table "properties" {
  schema = schema.public

  column "name" {
    null = false
    type = text
  }
  column "value" {
    null = false
    type = text
  }
  column "created_by" {
    null = true
    type = uuid
  }
  column "created_at" {
    null    = true
    type    = timestamptz
    default = sql("now()")
  }
  column "updated_at" {
    null    = true
    type    = timestamptz
    default = sql("now()")
  }
  column "deleted_at" {
    null = true
    type = timestamptz
  }

  primary_key {
    columns = [column.name]
  }

  index "properties_created_by_idx" {
    columns = [column.created_by]
  }
}

table "connections" {
  schema = schema.public

  column "id" {
    null    = false
    type    = text
    default = sql("generate_ulid()")
  }
  column "name" {
    null = false
    type = text
  }
  column "namespace" {
    null = true
    type = text
  }
  column "source" {
    null = true
    type = text
  }
  column "type" {
    null = false
    type = text
  }
  column "url" {
    null = true
    type = text
  }
  column "username" {
    null = true
    type = text
  }
  column "password" {
    null = true
    type = text
  }
  column "properties" {
    null = true
    type = jsonb
  }
  column "certificate" {
    null = true
    type = text
  }
  column "insecure_tls" {
    null    = true
    type    = boolean
    default = false
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
  column "created_by" {
    null = true
    type = text
  }

  primary_key {
    columns = [column.id]
  }

  index "connections_name_key" {
    unique  = true
    columns = [column.name]
  }
}

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
  column "params" {
    null = true
    type = jsonb
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

table "profiles" {
  schema = schema.public

  column "id" {
    null    = false
    type    = text
    default = sql("generate_ulid()")
  }
  column "name" {
    null = false
    type = text
  }
  column "namespace" {
    null = true
    type = text
  }
  column "spec" {
    null = false
    type = jsonb
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

  index "profiles_name_key" {
    unique  = true
    columns = [column.name]
  }
}

table "schedules" {
  schema = schema.public

  column "id" {
    null    = false
    type    = text
    default = sql("generate_ulid()")
  }
  column "name" {
    null = false
    type = text
  }
  column "namespace" {
    null = true
    type = text
  }
  column "spec" {
    null = false
    type = jsonb
  }
  column "enabled" {
    null    = false
    type    = boolean
    default = true
  }
  column "last_run" {
    null = true
    type = timestamptz
  }
  column "next_run" {
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

  index "schedules_name_key" {
    unique  = true
    columns = [column.name]
  }
}

table "schedule_runs" {
  schema = schema.public

  # id is the clicky task group id, so a persisted run and the live one the task
  # manager still holds are the same record rather than two views of it.
  column "id" {
    null = false
    type = text
  }
  column "schedule_name" {
    null = true
    type = text
  }
  column "name" {
    null = false
    type = text
  }
  column "kind" {
    null = true
    type = text
  }
  column "status" {
    null = false
    type = text
  }
  column "labels" {
    null = true
    type = jsonb
  }
  column "owner" {
    null = true
    type = text
  }
  column "total" {
    null    = false
    type    = int
    default = 0
  }
  column "completed" {
    null    = false
    type    = int
    default = 0
  }
  column "failed" {
    null    = false
    type    = int
    default = 0
  }
  column "running" {
    null    = false
    type    = int
    default = 0
  }
  # snapshots is the full group + child task snapshot slice, so a drill-down
  # survives a restart. The columns above are denormalised from it so a listing
  # never has to decode it.
  column "snapshots" {
    null = false
    type = jsonb
  }
  column "artifact_path" {
    null = true
    type = text
  }
  column "delivery" {
    null = true
    type = jsonb
  }
  column "started_at" {
    null = true
    type = timestamptz
  }
  column "finished_at" {
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

  index "schedule_runs_schedule_name_idx" {
    columns = [column.schedule_name]
  }

  index "schedule_runs_started_at_idx" {
    columns = [column.started_at]
  }
}

table "schedule_fires" {
  schema = schema.public

  column "id" {
    null    = false
    type    = text
    default = sql("generate_ulid()")
  }
  column "schedule_name" {
    null = false
    type = text
  }
  # scheduled_for is the cron instant this fire belongs to, which differs from
  # fired_at for a catch-up or a late scheduler.
  column "scheduled_for" {
    null = false
    type = timestamptz
  }
  column "fired_at" {
    null = false
    type = timestamptz
  }
  # A fire that produced no run is still a fact about the schedule: outcome
  # records skipped and failed alongside started.
  column "outcome" {
    null = false
    type = text
  }
  column "run_id" {
    null = true
    type = text
  }
  column "reason" {
    null = true
    type = text
  }
  column "error" {
    null = true
    type = text
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }

  primary_key {
    columns = [column.id]
  }

  index "schedule_fires_schedule_name_idx" {
    columns = [column.schedule_name, column.fired_at]
  }
}
