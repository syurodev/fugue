package hostconn

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/syurodev/fugue/pkg/client"
)

func versionParts(v string) ([3]uint64, bool) {
	var result [3]uint64
	v = strings.TrimPrefix(v, "v")
	pieces := strings.Split(v, ".")
	if len(pieces) != len(result) {
		return result, false
	}
	for i, part := range pieces {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, false
		}
		result[i] = n
	}
	return result, true
}

func versionAdvice(app, host string, counts client.Counts) string {
	a, aok := versionParts(app)
	h, hok := versionParts(host)
	if !aok || !hok {
		if app == host {
			return ""
		}
		return "Cannot compare host and app versions; update the app"
	}
	order := slices.Compare(h[:], a[:])
	v := "v" + strings.TrimPrefix(host, "v")
	switch {
	case order > 0:
		return fmt.Sprintf("Fugue host %s is newer; update the app", v)
	case order < 0 && (counts.Total != 1 || counts.App != 1):
		return fmt.Sprintf("Fugue host %s is busy; upgrade when it is idle", v)
	case order < 0:
		return fmt.Sprintf("Fugue host %s is older; restart it to upgrade", v)
	default:
		return ""
	}
}
