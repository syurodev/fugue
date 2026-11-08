package main

import (
	"os"

	"github.com/syurodev/fugue/pkg/client"
)

func main() {
	os.Exit(run())
}

func run() int {
	_ = client.Params{}
	return 0
}
