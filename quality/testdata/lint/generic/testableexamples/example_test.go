// lint:expect testableexamples

package testableexamples

import "fmt"

func ExampleNoOutput() { fmt.Fprintln(nil, "x") }
