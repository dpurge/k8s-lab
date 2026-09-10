package server

import (
	"encoding/json"
	"net/http"
)

type errorCode string

const (
	codeValidationFailed   errorCode = "validation_failed"
	codeInvalidCredentials errorCode = "invalid_credentials"
	codeInsufficientRole   errorCode = "insufficient_role"
	codeUnauthorized       errorCode = "unauthorized"
	codeForbidden          errorCode = "forbidden"
	codeNotFound           errorCode = "not_found"
	codeConflict           errorCode = "conflict"
	codeInternal           errorCode = "internal"
)

type errorBody struct {
	Code    errorCode `json:"code"`
	Message string    `json:"message"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body) //nolint:errcheck // headers already sent; nothing to do if encoding fails
}

func writeError(w http.ResponseWriter, status int, code errorCode, message string) {
	writeJSON(w, status, errorResponse{Error: errorBody{Code: code, Message: message}})
}

// decodeJSON returns false (having already written the error response) on
// malformed JSON — callers should stop handling the request in that case.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "malformed JSON body: "+err.Error())
		return false
	}
	return true
}
