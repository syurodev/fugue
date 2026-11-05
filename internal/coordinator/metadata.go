package coordinator

import (
	"reflect"

	"github.com/syurodev/fugue/internal/kernel"
)

func commandMetadata(c kernel.Command) (cmdID, runID, origin string, token uint64) {
	if c == nil {
		return "", "", "", 0
	}
	val := reflect.ValueOf(c)
	if val.Kind() == reflect.Pointer {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return "", "", "", 0
	}
	if f := val.FieldByName("CommandID"); f.IsValid() && f.Kind() == reflect.String {
		cmdID = f.String()
	}
	if f := val.FieldByName("RunID"); f.IsValid() && f.Kind() == reflect.String {
		runID = f.String()
	}
	if f := val.FieldByName("Origin"); f.IsValid() && f.Kind() == reflect.String {
		origin = f.String()
	}
	if f := val.FieldByName("Token"); f.IsValid() && f.Kind() == reflect.Uint64 {
		token = f.Uint()
	}
	return cmdID, runID, origin, token
}

func setCommandMetadata(c kernel.Command, cmdID, runID, origin string, token uint64) {
	if c == nil {
		return
	}
	val := reflect.ValueOf(c)
	if val.Kind() != reflect.Pointer {
		return
	}
	val = val.Elem()
	if val.Kind() != reflect.Struct {
		return
	}
	setStringField(val, "CommandID", cmdID)
	setStringField(val, "RunID", runID)
	setStringField(val, "Origin", origin)
	setUint64Field(val, "Token", token)
}

func setStringField(val reflect.Value, name, s string) {
	if s == "" {
		return
	}
	if f := val.FieldByName(name); f.IsValid() && f.CanSet() && f.Kind() == reflect.String {
		f.SetString(s)
	}
}

func setUint64Field(val reflect.Value, name string, u uint64) {
	if u == 0 {
		return
	}
	if f := val.FieldByName(name); f.IsValid() && f.CanSet() && f.Kind() == reflect.Uint64 {
		f.SetUint(u)
	}
}
