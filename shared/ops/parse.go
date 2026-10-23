package ops

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"k8s-lab/shared/message"
)

// The Parse functions turn the payload of a message into a typed value. They
// read strictly: an unknown field, a field of the wrong type, data after the
// object, or a broken rule is an error. The errors are *message.Error values
// with the code invalid_payload and a text that names the field, never its
// value.

// ParseDocumentsSearchRequest reads the payload of a documents_search request.
func ParseDocumentsSearchRequest(payload json.RawMessage) (DocumentsSearchRequest, error) {
	return parse[DocumentsSearchRequest](payload)
}

// ParseDocumentsSearchResponse reads the payload of a documents_search response.
func ParseDocumentsSearchResponse(payload json.RawMessage) (DocumentsSearchResponse, error) {
	return parse[DocumentsSearchResponse](payload)
}

// ParseURLIngestRequest reads the payload of a url_ingest request.
func ParseURLIngestRequest(payload json.RawMessage) (URLIngestRequest, error) {
	return parse[URLIngestRequest](payload)
}

// ParseURLIngestResponse reads the payload of a url_ingest response.
func ParseURLIngestResponse(payload json.RawMessage) (URLIngestResponse, error) {
	return parse[URLIngestResponse](payload)
}

// ParseTaskStatusRequest reads the payload of a task_status request.
func ParseTaskStatusRequest(payload json.RawMessage) (TaskStatusRequest, error) {
	return parse[TaskStatusRequest](payload)
}

// ParseTaskStatusResponse reads the payload of a task_status response.
func ParseTaskStatusResponse(payload json.RawMessage) (TaskStatusResponse, error) {
	return parse[TaskStatusResponse](payload)
}

type checked interface{ validate() error }

func parse[T checked](payload json.RawMessage) (T, error) {
	var zero, value T
	if len(bytes.TrimSpace(payload)) == 0 {
		return zero, invalidPayload("payload is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return zero, invalidPayload("payload must be one JSON object with only the documented fields, of the documented types")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return zero, invalidPayload("payload has data after the object")
	}
	if err := value.validate(); err != nil {
		return zero, invalidPayload(err.Error())
	}
	return value, nil
}

func invalidPayload(reason string) *message.Error {
	return &message.Error{Code: message.CodeInvalidPayload, Message: "payload is not valid: " + reason}
}
