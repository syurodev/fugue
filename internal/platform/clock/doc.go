// Package clock is the time port: a Clock for reading time and waiting, a real
// implementation and a fake one that tests advance by hand.
//
// Owner: time port (docs/quality-gates.md section 3). It is the only package
// that may call time.Now, time.NewTimer and friends. Allowed callers: every
// package that reads or waits on time, through an injected Clock.
package clock
