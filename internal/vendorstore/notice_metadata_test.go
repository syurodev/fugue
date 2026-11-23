package vendorstore

import "testing"

// TestVRE14GGE05NoticeMetadataAllowlistHoldsCountsOnly: a possible_secret
// notice may carry counts and the incomplete flag in metadata, and no other
// kind may carry them.
func TestVRE14GGE05NoticeMetadataAllowlistHoldsCountsOnly(t *testing.T) {
	t.Parallel()
	notice := Item{Kind: KindNotice, Metadata: ItemMetadata{Level: "warning", Code: "possible_secret", FindingCount: 2, FindingFiles: 1, ScanIncomplete: true}}
	if err := validateItemMetadata(&notice); err != nil {
		t.Fatalf("notice counts must be allowed: %v", err)
	}
	other := Item{Kind: KindTurnDiff, Metadata: ItemMetadata{Status: "complete", FindingCount: 1}}
	if err := validateItemMetadata(&other); err == nil {
		t.Fatal("a finding count on a turn_diff item must be rejected")
	}
}
