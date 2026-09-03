package response

import (
	"encoding/json"
	"errors"
	"net/http"
)

type Error struct {
	Code, Message string
	Status        int
	Details       any
}

func (e *Error) Error() string { return e.Message }
func E(status int, code, message string) error {
	return &Error{Code: code, Message: message, Status: status, Details: map[string]any{}}
}

var (
	ErrUnauthorized = E(http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
	ErrForbidden    = E(http.StatusForbidden, "FORBIDDEN", "Permission denied")
	ErrNotFound     = E(http.StatusNotFound, "NOT_FOUND", "Resource not found")
)

func JSON(w http.ResponseWriter, status int, data any, meta any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if meta == nil {
		meta = map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "meta": meta})
}
func WriteError(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Status: 500, Code: "INTERNAL_ERROR", Message: "Internal server error", Details: map[string]any{}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": e.Code, "message": e.Message, "details": e.Details}})
}
func Decode(w http.ResponseWriter, r *http.Request, dst any, max int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return &Error{Status: 400, Code: "INVALID_JSON", Message: "Invalid request body", Details: map[string]any{"reason": err.Error()}}
	}
	return nil
}
