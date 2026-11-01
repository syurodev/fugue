// lint:expect rowserrcheck

package rowserrcheck

import "database/sql"

func NoErr(db *sql.DB) error {
	rows, err := db.Query("select 1")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return nil
}
