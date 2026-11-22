package todos

// TODO(FUG-12): follow up on a Linear issue.
func Linear() {}

// TODO(F9.9): follow up on a spec item.
func Spec() {}

/* TODO(F11.5c): follow up on a spec item with a suffix. */
func SpecSuffix() {}

/* TODO: no item */ // want `[T]ODO must use [T]ODO\(FUG-<n>\):`
func Bare()         {}

/* TODO(soon): no item ID */ // want `[T]ODO must use`
func Soon()                  {}

/* TODO(FUG-12) no colon */ // want `[T]ODO must use`
func NoColon()              {}

/* TODO(FUG-12a): Linear issue IDs take only digits. */ // want `[T]ODO must use`
func LinearSuffix()                                     {}

/* TODO(F11.5cc): spec item IDs take at most one suffix letter. */ // want `[T]ODO must use`
func LongSpecSuffix()                                              {}

/* TODO(F11.5C): spec item ID suffixes are lowercase. */ // want `[T]ODO must use`
func UppercaseSpecSuffix()                               {}

/* FIXME(FUG-12): broken */ // want `[F]IXME is not allowed`
func Fixme()                {}

/* HACK: ugly */ // want `[H]ACK is not allowed`
func Hack()      {}

/* XXX: look */ // want `[X]XX is not allowed`
func Xxx()      {}

// The words todo and todoref in lower case, and TODOs, are prose.
func Prose() {}
