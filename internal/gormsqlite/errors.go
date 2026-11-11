// Derived from github.com/clarkmcc/gorm-sqlite at 00ed082c0311.
// Copyright (c) 2013-NOW Jinzhu. See License for the MIT license.

package sqlite

import "errors"

var (
	ErrConstraintsNotImplemented = errors.New("constraints not implemented on sqlite, consider using DisableForeignKeyConstraintWhenMigrating, more details https://github.com/go-gorm/gorm/wiki/GORM-V2-Release-Note-Draft#all-new-migrator")
)
