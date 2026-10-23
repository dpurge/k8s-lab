package message

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Decode parses one encoded envelope. It checks the size before it parses, and
// it reads strictly: an unknown field, a field of the wrong type or anything
// after the envelope is an error. It does not check the envelope's rules (see
// Validate). The errors are *Error values.
func Decode(data []byte) (Envelope, error) {
	if len(data) > MaxEncodedBytes {
		return Envelope{}, newError(CodeTooLarge, "message is larger than the maximum encoded size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var env Envelope
	if err := decoder.Decode(&env); err != nil {
		return Envelope{}, newError(CodeInvalidMessage, "message is not a valid envelope: it must be one JSON object with only the documented fields")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Envelope{}, newError(CodeInvalidMessage, "message has data after the envelope")
	}
	return env, nil
}

// Encode writes an envelope as compact JSON, leaving <, > and & as they are so
// a payload stays readable. It refuses an envelope larger than MaxEncodedBytes;
// it does not check the other rules (see Validate).
func Encode(env Envelope) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(env); err != nil {
		return nil, newError(CodeInvalidMessage, "envelope cannot be encoded: its payload is not valid JSON")
	}
	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(data) > MaxEncodedBytes {
		return nil, newError(CodeTooLarge, "message is larger than the maximum encoded size")
	}
	return data, nil
}
