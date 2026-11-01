package a

type NopLogger struct{}

func (NopLogger) Log(string) {}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func NopClose() {}

func Real(x int) (int, error) { return x + 1, nil }

func Single() error { return nil }
