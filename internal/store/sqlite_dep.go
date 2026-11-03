package store

// The database driver (pure Go, no cgo). It registers itself under the name "sqlite" for database/sql.
import _ "modernc.org/sqlite"
