// lint:expect errcheck

package errcheck

func Assert(v any) string { return v.(string) }
