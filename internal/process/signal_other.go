//go:build unix && !darwin

package process

func platformGroupSignalError(_ int, err error) error { return err }

func platformLeaderSignalError(_ int, err error) error { return err }
