package errcode

type Code string

const (
	NotFound Code = "not_found"
	Forgotten Code = "forgotten"
	Both Code = "both"
	SecretDetail Code = "secret_detail"
)
