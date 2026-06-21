package main

import (
	"fmt"

	"gorm.io/gorm"
)

// pgulidSQL installs generate_ulid(), the id default for commons-db models. A
// standalone deployment owns its database and must provide it (duty installs the
// same function via its migrations). This builds a ULID-shaped uuid: 6 bytes of
// millisecond timestamp (sortable) followed by 10 random bytes.
const pgulidSQL = `
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
`

// ensureSchema installs the database functions the models depend on. It must run
// before AutoMigrate, whose column DDL references generate_ulid().
func ensureSchema(db *gorm.DB) error {
	if err := db.Exec(pgulidSQL).Error; err != nil {
		return fmt.Errorf("install generate_ulid(): %w", err)
	}
	return nil
}
