package redact

import "fmt"

type Secret string

func (s Secret) Redacted() string { return "[redacted]" }

func String(s string) string { return "[redacted]" }

func Bytes(b []byte) string { return "[redacted]" }

func debug(s Secret) { fmt.Println(s) }
