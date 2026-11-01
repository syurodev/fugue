package coordinator

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

var _ *sql.DB

const Peek = "SELECT 1 FROM exec_events"
