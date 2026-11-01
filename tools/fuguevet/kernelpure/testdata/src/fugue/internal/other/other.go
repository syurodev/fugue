package other

import (
	"fmt"
	"os"
	"time"
)

func Free() {
	go func() {}()
	fmt.Println(os.Args, time.Now())
}

var Mutable = 1
