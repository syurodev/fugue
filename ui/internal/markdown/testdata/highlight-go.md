```go
package main

import "fmt"

const answer = 42

func main() {
	var s string = "plain"
	if s == "" && true {
		fmt.Printf("%d\n", answer)
	}
}
```
