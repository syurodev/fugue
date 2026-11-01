package errcode

type Code string

const (
	NotFound       Code = "not_found"
	InternalDetail Code = "internal_detail"
)
