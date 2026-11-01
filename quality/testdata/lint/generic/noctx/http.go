// lint:expect noctx

package noctx

import "net/http"

func Fetch() (*http.Response, error) { return http.Get("http://localhost") }
