package process

import "testing"

func TestSignalPlanSkipsReapedLeader(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		leaderExited    bool
		groupHasMembers bool
		want            signalTargets
	}{
		{name: "leader alive signals group and pid", leaderExited: false, groupHasMembers: true, want: signalTargets{group: true, leader: true}},
		{name: "leader exited with members signals group only", leaderExited: true, groupHasMembers: true, want: signalTargets{group: true, leader: false}},
		{name: "leader exited and group empty signals nothing", leaderExited: true, groupHasMembers: false, want: signalTargets{group: false, leader: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := planSignal(tc.leaderExited, tc.groupHasMembers); got != tc.want {
				t.Fatalf("planSignal(%v, %v) = %+v, want %+v", tc.leaderExited, tc.groupHasMembers, got, tc.want)
			}
		})
	}
}
