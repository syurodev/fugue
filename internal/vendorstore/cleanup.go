package vendorstore

import "database/sql"

func rollback(tx *sql.Tx) {
	if operationErr1 := tx.Rollback(); operationErr1 != nil {
		return
	}
}

func closeRows(rows *sql.Rows) {
	if operationErr2 := rows.Close(); operationErr2 != nil {
		return
	}
}
