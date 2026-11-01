package todos

// TODO(F9.1): the live item is allowed.
func Allowed() {}

/* TODO(F9.2): the item is done */ // want `[T]ODO\(F9.2\) refers to an item that is already Done`
func Done()                        {}

/* TODO(F9.9): nobody knows this item */ // want `[T]ODO\(F9.9\) names an item that is not in docs/roadmap.md`
func Unknown()                           {}

/* TODO: no item */ // want `[T]ODO must read [T]ODO\(F<phase>.<n>\)`
func Bare()         {}

/* TODO(soon): no item id */ // want `[T]ODO must read`
func Soon()                  {}

/* TODO(F9.1) no colon */ // want `[T]ODO must read`
func NoColon()            {}

/* FIXME: broken */ // want `[F]IXME is not allowed`
func Fixme()        {}

/* HACK: ugly */ // want `[H]ACK is not allowed`
func Hack()      {}

/* XXX: look */ // want `[X]XX is not allowed`
func Xxx()      {}

// The words todo and todoref in lower case, and TODOs, are prose.
func Prose() {}
