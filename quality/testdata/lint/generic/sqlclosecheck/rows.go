// lint:expect sqlclosecheck

package sqlclosecheck

import "database/sql"

func Leak(db *sql.DB) error {
	rows, err := db.Query("select 1")
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	return rows.Err()
}
