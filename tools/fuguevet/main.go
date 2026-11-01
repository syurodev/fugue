package main

import (
	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/syurodev/fugue/tools/fuguevet/kernelpure"
	"github.com/syurodev/fugue/tools/fuguevet/nobarego"
	"github.com/syurodev/fugue/tools/fuguevet/noprotoleak"
	"github.com/syurodev/fugue/tools/fuguevet/secretlog"
	"github.com/syurodev/fugue/tools/fuguevet/todoref"
)

func main() {
	unitchecker.Main(
		kernelpure.New(),
		nobarego.New(),
		secretlog.New(),
		todoref.New(),
		noprotoleak.New(),
	)
}
