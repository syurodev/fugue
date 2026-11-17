package wall

import "time"

// Production code may read the wall clock; the rule covers test files only.
func Stamp() time.Time { return time.Now() }
