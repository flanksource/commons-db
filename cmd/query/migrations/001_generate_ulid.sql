-- phase: pre

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE OR REPLACE FUNCTION generate_ulid() RETURNS uuid AS $$
DECLARE
  unix_time BIGINT;
  ulid      BYTEA;
BEGIN
  unix_time = (EXTRACT(EPOCH FROM NOW()) * 1000)::BIGINT;
  ulid = decode(lpad(to_hex(unix_time), 12, '0'), 'hex') || gen_random_bytes(10);
  RETURN encode(ulid, 'hex')::uuid;
END
$$ LANGUAGE plpgsql VOLATILE;
