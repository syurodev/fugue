// Package vendor defines the neutral vendor domain port: the Adapter and
// Session contracts every vendor CLI implements, the capability declaration,
// and the input rules shared by all adapters (vendor-runtime §6).
// Owner: vendor runtime (VR-01, VR-07). Allowed callers: the adapter
// subpackages (claude, codex, agy), internal/app (the session actor) and tests.
package vendor
