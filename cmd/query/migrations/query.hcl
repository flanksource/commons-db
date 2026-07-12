schema "public" {
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
