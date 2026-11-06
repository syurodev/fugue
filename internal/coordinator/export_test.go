package coordinator

// WithOnFire registers a hook called whenever a timer fire successfully commits or completes.
func WithOnFire(fn func(*ArmedTimer)) SchedulerOption {
	return func(s *Scheduler) {
		s.onFire = fn
	}
}

// WithOnArm registers a hook called whenever a timer is processed into the scheduler.
func WithOnArm(fn func(*ArmedTimer)) SchedulerOption {
	return func(s *Scheduler) {
		s.onArm = fn
	}
}

// WithOnParked registers a hook called whenever a timer is parked.
func WithOnParked(fn func(*ArmedTimer)) SchedulerOption {
	return func(s *Scheduler) {
		s.onParked = fn
	}
}

// WithOnDropped registers a hook called whenever a timer is dropped on definite rejection.
func WithOnDropped(fn func(*ArmedTimer)) SchedulerOption {
	return func(s *Scheduler) {
		s.onDropped = fn
	}
}
