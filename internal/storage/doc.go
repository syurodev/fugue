// Package storage opens the local SQLite database (fugue.db), verifies
// durability pragmas (journal_mode=DELETE, synchronous=FULL, file mode 0600),
// runs forward-only schema migrations from embedded SQL files, acquires the
// exclusive data-directory lock (fugue.lock) and records the monotonic host
// incarnation in exec_host.
//
// Owner: storage (docs/adr/0001-package-layout.md, G-03). Allowed callers:
// internal/vault, internal/credentials, internal/execstore,
// internal/workspacememory and internal/app.
package storage
