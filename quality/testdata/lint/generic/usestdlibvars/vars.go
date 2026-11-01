// lint:expect usestdlibvars

package usestdlibvars

import "net/http"

func Status(w http.ResponseWriter) { w.WriteHeader(200) }
