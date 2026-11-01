// lint:expect exhaustruct_v5 SendRequest

package hostapi

type SendRequest struct {
	Text string
	Mode string
}

func IncompleteRequest() SendRequest { return SendRequest{Text: "t"} }
