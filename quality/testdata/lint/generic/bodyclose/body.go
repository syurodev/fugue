// lint:expect bodyclose

package bodyclose

import "net/http"

func Leak(req *http.Request) int {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	return resp.StatusCode
}
