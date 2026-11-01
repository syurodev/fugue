package execstore

import "database/sql"

var _ *sql.DB

const Query = "SELECT id FROM exec_events"
