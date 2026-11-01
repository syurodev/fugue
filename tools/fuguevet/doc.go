// Command fuguevet is the repository's custom `go vet` tool. It bundles the
// analyzers of docs/quality-gates.md section 4: kernelpure, nobarego, secretlog,
// todoref and noprotoleak.
//
// Owner: quality gates. It is a development tool run as
// `go vet -vettool=bin/fuguevet ./...` by Taskfile.yml and CI; nothing imports it.
package main
