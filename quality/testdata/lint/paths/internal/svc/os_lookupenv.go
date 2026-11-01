// lint:expect forbidigo os.LookupEnv

package svc

import "os"

func OsLookupEnv() (string, bool) { return os.LookupEnv("X") }
