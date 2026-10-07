package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// Error codes of the JSON error envelope (docs/architecture.md §6).
const (
	CodeValidation   = "validation"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeRateLimited  = "rate_limited"
	CodeUnavailable  = "unavailable"
	CodeInternal     = "internal"
	CodeBadRequest   = "bad_request"
)

// APIError is returned by handlers and rendered as
// {"error":{"code":..., "message":..., "details":...}}. Message is Russian and user-facing.
type APIError struct {
	Status  int
	Code    string
	Message string
	Details any
	Err     error // internal cause, logged, never sent to the client
}

func (e *APIError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return e.Code + ": " + e.Message
}

func (e *APIError) Unwrap() error { return e.Err }

// Constructors for common errors.
func errValidation(msg string, details any) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: CodeValidation, Message: msg, Details: details}
}
func errBadRequest(msg string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: CodeBadRequest, Message: msg}
}
func errNotFound(msg string) *APIError {
	if msg == "" {
		msg = "Не найдено"
	}
	return &APIError{Status: http.StatusNotFound, Code: CodeNotFound, Message: msg}
}
func errConflict(msg string, details any) *APIError {
	return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: msg, Details: details}
}
func errUnauthorized() *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: "Нужно войти в систему"}
}
func errForbidden(msg string) *APIError {
	if msg == "" {
		msg = "Недостаточно прав для этого действия"
	}
	return &APIError{Status: http.StatusForbidden, Code: CodeForbidden, Message: msg}
}
func errInternal(err error) *APIError {
	return &APIError{Status: http.StatusInternalServerError, Code: CodeInternal,
		Message: "Внутренняя ошибка сервера. Попробуйте ещё раз или обратитесь к администратору.", Err: err}
}

// handlerFunc is an http handler that may return an error to be rendered.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// writeJSON writes v as JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

// writeError renders any error as the JSON envelope; non-APIError errors become 500.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var ae *APIError
	if !errors.As(err, &ae) {
		ae = errInternal(err)
	}
	if ae.Status >= 500 {
		log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	} else if ae.Err != nil {
		log.Debug("request rejected", "method", r.Method, "path", r.URL.Path, "code", ae.Code, "err", ae.Err)
	}
	body := map[string]any{"code": ae.Code, "message": ae.Message}
	if ae.Details != nil {
		body["details"] = ae.Details
	}
	writeJSON(w, ae.Status, map[string]any{"error": body})
}

// maxJSONBody limits request bodies of JSON endpoints.
const maxJSONBody = 1 << 20

// decodeJSON reads a JSON request body into dst. Unknown fields are allowed
// (forward compatible clients); malformed JSON is a 400 with a Russian message.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return &APIError{Status: http.StatusUnsupportedMediaType, Code: CodeBadRequest, Message: "Ожидается JSON"}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errBadRequest("Пустой запрос")
		}
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return &APIError{Status: http.StatusRequestEntityTooLarge, Code: CodeBadRequest, Message: "Слишком большой запрос"}
		}
		return &APIError{Status: http.StatusBadRequest, Code: CodeBadRequest, Message: "Некорректный формат запроса", Err: err}
	}
	return nil
}

// pathID parses a positive int64 path value like {id}.
func pathID(r *http.Request, name string) (int64, error) {
	v := r.PathValue(name)
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, errNotFound("")
	}
	return id, nil
}

// queryInt64 parses an optional int64 query parameter.
func queryInt64(r *http.Request, name string) (*int64, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil, errBadRequest("Некорректный параметр " + name)
	}
	return &n, nil
}
