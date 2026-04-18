package handler

import "net/http"

// healthBody is hardcoded — zero allocations, exact bytes, no json.Marshal risk.
// S6 checks the body literally: {"status":"ok"} and nothing else.
var healthBody = []byte(`{"status":"ok"}`)

func Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(healthBody) //nolint:errcheck
}
