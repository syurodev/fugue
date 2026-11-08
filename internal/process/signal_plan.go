package process

// signalTargets says which ends of a session tree a stop step may signal.
type signalTargets struct {
	group  bool
	leader bool
}

// planSignal decides the targets of one stop step. The leader pid is
// signaled directly only while the leader has not been reaped: after Wait
// the pid may already belong to an unrelated process. The group is signaled
// only while a probe in the same step shows it still has members, because
// an empty group's id is free for reuse as well.
func planSignal(leaderExited, groupHasMembers bool) signalTargets {
	if !leaderExited {
		return signalTargets{group: true, leader: true}
	}
	return signalTargets{group: groupHasMembers, leader: false}
}
