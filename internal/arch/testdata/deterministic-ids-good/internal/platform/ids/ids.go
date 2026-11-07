package ids

// NewDeterministic stands in for the real generator of the same name.
func NewDeterministic(seed string) int { return len(seed) }
