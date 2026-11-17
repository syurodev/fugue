// Package cheapvault is the one vault fixture of the tests: a keyring made with
// the lowest Argon2id cost the format allows, so a fixture exercises a real
// locked-then-unlocked vault without paying the production cost (64 MiB, three
// passes, three derivations in Setup), which dominates run time under the race
// detector. The fuguevet analyzer cheapvault rejects production-cost setup in
// every other test file.
//
// Owner: quality gates (docs/quality-gates.md section 6). Allowed callers:
// test files only; the arch tests reject production imports.
package cheapvault
