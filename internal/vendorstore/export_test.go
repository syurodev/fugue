package vendorstore

// SetCommitFault injects a pre-commit storage failure for atomicity tests.
func (s *Store) SetCommitFault(err error) {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	s.beforeCommit = err
}
