package main

import "github.com/syurodev/fugue/internal/platform/ids"

func main() {
	_ = ids.NewDeterministic("conn")
}
