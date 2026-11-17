package vault

type Service struct{}

func (*Service) Setup(password string) error { return nil }
