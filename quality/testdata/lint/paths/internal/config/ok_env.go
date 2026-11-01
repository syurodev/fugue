// lint:ok forbidigo

package config

import "os"

func Home() string { return os.Getenv("HOME") }
