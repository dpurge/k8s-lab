package message

import (
	"encoding/json"
	"regexp"
	"time"
	"unicode/utf8"
)

const (
	maxSummaryLength = 500
	maxMessageLength = 1000
)

var (
	uuidRe      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	appNameRe   = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	operationRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// Validate checks the envelope's rules. With a registry it also checks the
// payload against the schema of the operation: a request for an operation the
// registry does not know is unknown_operation, and a payload that breaks its
// schema is invalid_payload. Responses are checked against the response schema
// when the operation is registered; events and errors carry no schema. The
// errors are *Error values.
func Validate(env Envelope, reg *Registry) error {
	if err := validateEnvelope(env); err != nil {
		return err
	}
	if reg == nil {
		return nil
	}
	switch env.Type {
	case TypeRequest:
		return reg.ValidateRequestPayload(env.Operation, env.Payload)
	case TypeResponse:
		if !reg.Has(env.Operation) {
			return nil
		}
		return reg.ValidateResponsePayload(env.Operation, env.Payload)
	}
	return nil
}

func validateEnvelope(env Envelope) error {
	invalid := func(message string) error { return newError(CodeInvalidMessage, message) }

	if env.SchemaVersion != SchemaVersion {
		return invalid("schema_version must be 1")
	}
	if !uuidRe.MatchString(env.ID) {
		return invalid("id must be a lower-case UUID")
	}
	switch env.Type {
	case TypeRequest, TypeResponse, TypeEvent, TypeError:
	default:
		return invalid("type must be request, response, event or error")
	}
	if !uuidRe.MatchString(env.CorrelationID) {
		return invalid("correlation_id must be a lower-case UUID")
	}
	if env.Type == TypeRequest && env.CorrelationID != env.ID {
		return invalid("a request's correlation_id must equal its id")
	}
	if !appNameRe.MatchString(env.From) {
		return invalid("from must be 1 to 64 characters of a-z, 0-9, _ and -")
	}
	if !appNameRe.MatchString(env.To) {
		return invalid("to must be 1 to 64 characters of a-z, 0-9, _ and -")
	}
	if !isUTCTimestamp(env.CreatedAt) {
		return invalid("created_at must be an RFC 3339 time in UTC, ending in Z")
	}
	if !operationRe.MatchString(env.Operation) {
		return invalid("operation must be 1 to 64 characters, start with a-z and use only a-z, 0-9 and _")
	}
	if utf8.RuneCountInString(env.Summary) > maxSummaryLength {
		return invalid("summary must be at most 500 characters")
	}
	if err := validateErrorBody(env); err != nil {
		return err
	}
	return validatePayloadShape(env)
}

func validateErrorBody(env Envelope) error {
	invalid := func(message string) error { return newError(CodeInvalidMessage, message) }

	if env.Type != TypeError {
		if env.Error != nil {
			return invalid("error is only allowed on a message of type error")
		}
		return nil
	}
	if env.Error == nil {
		return invalid("a message of type error must have an error object")
	}
	if !knownCodes[env.Error.Code] {
		return invalid("error.code must be one of the documented codes")
	}
	if env.Error.Message == "" {
		return invalid("error.message must not be empty")
	}
	if utf8.RuneCountInString(env.Error.Message) > maxMessageLength {
		return invalid("error.message must be at most 1000 characters")
	}
	return nil
}

// validatePayloadShape checks that a payload, when there is one, is a JSON
// object, and that a request has one.
func validatePayloadShape(env Envelope) error {
	if len(env.Payload) == 0 {
		if env.Type == TypeRequest {
			return newError(CodeInvalidMessage, "a request must have a payload object")
		}
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &object); err != nil || object == nil {
		return newError(CodeInvalidMessage, "payload must be a JSON object")
	}
	return nil
}

func isUTCTimestamp(s string) bool {
	if len(s) == 0 || s[len(s)-1] != 'Z' {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}
