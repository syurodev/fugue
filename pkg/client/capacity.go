package client

// codeVendorCapacity is the public code of a start over the session cap.
const codeVendorCapacity = "vendor.capacity"

// CapacityScope names the session cap a refused start ran into.
type CapacityScope string

// The scopes the host reports: the machine-wide cap or one account's cap.
const (
	CapacityScopeMachine CapacityScope = "machine"
	CapacityScopeAccount CapacityScope = "account"
)

// Capacity is the detail of a vendor.capacity refusal: the sessions counted
// against the cap that refused the start and that cap. The host sends plain
// numbers only.
type Capacity struct {
	Running int
	Limit   int
	Scope   CapacityScope
}

// VendorCapacityOf returns the capacity detail of a start the host refused
// with vendor.capacity, and whether err is such a refusal. A refusal from a
// host that sent no counts reports a zero Limit.
func VendorCapacityOf(err error) (Capacity, bool) {
	detail, ok := errorDetailOf(err)
	if !ok || detail.GetCode() != codeVendorCapacity {
		return Capacity{}, false
	}
	return Capacity{
		Running: int(detail.GetRunningSessions()),
		Limit:   int(detail.GetSessionLimit()),
		Scope:   CapacityScope(detail.GetSessionLimitScope()),
	}, true
}
