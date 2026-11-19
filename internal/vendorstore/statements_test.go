package vendorstore

import (
	"database/sql"
	"errors"
	"testing"
)

func TestPrepareAndRunPreparesQueriesOnceAndClosesWhenDone(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	requireNoError(t, err)
	defer rollback(t.Context(), tx)
	stmts := newTxStatements(tx)
	var prepared *sql.Stmt
	err = prepareAndRun(t.Context(), tx, stmts, []string{rejectPurgedQuery}, func() error {
		prepared = stmts.prepared[rejectPurgedQuery]
		if prepared == nil || len(stmts.prepared) != 1 {
			t.Fatalf("prepared %d statements, got %v; want 1", len(stmts.prepared), prepared != nil)
		}
		for range 4 {
			var purged int
			requireNoError(t, stmts.QueryRowContext(t.Context(), rejectPurgedQuery, "s").Scan(&purged))
		}
		return nil
	})
	requireNoError(t, err)
	// prepareAndRun closes its prepared statements when fn returns, before
	// commit, so none outlives the batch or transaction.
	var purged int
	if err := prepared.QueryRowContext(t.Context(), "s").Scan(&purged); err == nil {
		t.Fatal("statement still usable after prepareAndRun returned")
	}
	requireNoError(t, tx.Commit())
}

func TestTxStatementsLeaveUnpreparedQueriesOnTx(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	requireNoError(t, err)
	defer rollback(t.Context(), tx)
	stmts := newTxStatements(tx)
	var purged int
	requireNoError(t, stmts.QueryRowContext(t.Context(), rejectPurgedQuery, "s").Scan(&purged))
	if len(stmts.prepared) != 0 {
		t.Fatalf("prepared %d statements for single-use tx, want 0", len(stmts.prepared))
	}
}

func TestPrepareAndRunSurfacesPrepareFailure(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	requireNoError(t, err)
	defer rollback(t.Context(), tx)
	stmts := newTxStatements(tx)
	const bad = "SELECT missing FROM no_such_table"
	if err := prepareAndRun(t.Context(), tx, stmts, []string{bad}, func() error {
		t.Fatal("callback ran after prepare failure")
		return nil
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
	var value int
	if err := stmts.QueryRowContext(t.Context(), bad).Scan(&value); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unprepared query row of invalid SQL: got %v, want SQL error", err)
	}
}
