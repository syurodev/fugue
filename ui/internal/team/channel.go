package team

import (
	"cmp"
	"slices"

	"github.com/syurodev/fugue/pkg/client"
)

// MergeChannel preserves paged history while replacing replayed lines by number.
func MergeChannel(current, delivered []client.ChannelLine) []client.ChannelLine {
	index := make(map[int64]client.ChannelLine, len(current)+len(delivered))
	for i := range current {
		index[current[i].ChannelSeq] = current[i]
	}
	for i := range delivered {
		index[delivered[i].ChannelSeq] = delivered[i]
	}
	out := make([]client.ChannelLine, 0, len(index))
	for seq := range index {
		out = append(out, index[seq])
	}
	slices.SortFunc(out, func(a, b client.ChannelLine) int { return cmp.Compare(a.ChannelSeq, b.ChannelSeq) })
	return out
}
