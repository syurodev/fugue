// lint:expect forbidigo os.Getenv

package svc

import "os"

func OsGetenv() string { return os.Getenv("X") }
