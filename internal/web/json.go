package web

import (
	"encoding/json"
	"net/http"
)

// json.go holds the minimal JSON request/response helpers for the /api/*
// surface. The HTML pages render templates; the API speaks JSON, and these
// keep the handlers terse without pulling in a framework.

// writeJSON encodes v as a JSON response with the given status code. Encoding
// failures are logged-by-omission (the header is already written); callers
// pass marshalable values.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// writeJSONError writes a {"error": code, "message": msg} body with status.
// code is a short machine-readable token (e.g. "not_linked") the browser can
// switch on; message is human-facing detail.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

// decodeJSON decodes the request body into v, rejecting unknown fields so a
// typo in a client payload surfaces as a 400 rather than being silently
// dropped. The 1 MiB smallBodyLimit middleware already bounds the body size.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
