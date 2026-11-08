package vendorstore

// SetCommitFault injects a pre-commit storage failure for atomicity tests.
func (s *Store) SetCommitFault(err error) {
	s.prepare.Lock()
	defer s.prepare.Unlock()
	s.beforeCommit = err
}
