package ops

import (
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"
)

// The limits of the payloads; the JSON Schemas under schemas/ carry the same
// numbers.
const (
	MaxQueryChars     = 2000
	MaxTags           = 20
	MaxTagChars       = 64
	MinLimit          = 1
	MaxLimit          = 20
	MaxDocuments      = 20
	MaxURLChars       = 2048
	MaxTaskIDChars    = 128
	MaxTaskErrorChars = 1000
)

// In the types below an optional field that is left out of the JSON and one that
// holds its zero value ("" or 0) read the same in Go. Only the JSON Schema can
// tell them apart, and the registry applies it in message.Validate: so
// "limit": 0 and "start": "" are refused on the wire, not by the Parse
// functions.

// DocumentsSearchRequest is the payload of a documents_search request. Limit is
// 1 to 20 and the responder uses 5 when it is 0.
type DocumentsSearchRequest struct {
	Query  string   `json:"query"`
	Tags   []string `json:"tags,omitempty"`
	Start  string   `json:"start,omitempty"`
	End    string   `json:"end,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Offset int      `json:"offset,omitempty"`
}

// Document is one found document.
type Document struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags"`
	Score   float64  `json:"score"`
	URL     string   `json:"url"`
}

// DocumentsSearchResponse is the payload of a documents_search response. Total,
// when set, is the number of matches before Limit and Offset.
type DocumentsSearchResponse struct {
	Documents []Document `json:"documents"`
	Total     int        `json:"total,omitempty"`
}

// URLIngestRequest is the payload of a url_ingest request.
type URLIngestRequest struct {
	URL  string   `json:"url"`
	Tags []string `json:"tags,omitempty"`
}

// URLIngestResponse is the payload of a url_ingest response: the task the app
// persisted before it answered, and the state it reports right after accepting
// the work (for example queued).
type URLIngestResponse struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

// TaskStatusRequest is the payload of a task_status request.
type TaskStatusRequest struct {
	ID string `json:"id"`
}

// TaskStatusResponse is the payload of a task_status response. It has no field
// for a task's payload or result on purpose: they can hold user text. Progress
// is set only where the app has one; Error is empty when there is none and is
// the app's own text. CreatedAt and UpdatedAt are RFC 3339 times in UTC ending
// in Z.
type TaskStatusResponse struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Step      string `json:"step,omitempty"`
	Progress  string `json:"progress,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

var (
	// timestampRe has the shape the JSON Schemas check; timestamp also parses it.
	timestampRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z$`)
	urlRe       = regexp.MustCompile(`^https?://[^\s]+$`)
)

// validate methods return the first broken rule, worded with the field name and
// never with its value.

func (r DocumentsSearchRequest) validate() error {
	if err := checkChars("query", r.Query, 1, MaxQueryChars); err != nil {
		return err
	}
	if err := checkTags(r.Tags); err != nil {
		return err
	}
	if err := checkOptionalTimestamp("start", r.Start); err != nil {
		return err
	}
	if err := checkOptionalTimestamp("end", r.End); err != nil {
		return err
	}
	if r.Limit != 0 && (r.Limit < MinLimit || r.Limit > MaxLimit) {
		return fmt.Errorf("limit must be from %d to %d", MinLimit, MaxLimit)
	}
	if r.Offset < 0 {
		return errors.New("offset must not be negative")
	}
	return nil
}

func (r DocumentsSearchResponse) validate() error {
	if r.Documents == nil {
		return errors.New("documents is required")
	}
	if len(r.Documents) > MaxDocuments {
		return fmt.Errorf("documents has more than %d items", MaxDocuments)
	}
	for i, d := range r.Documents {
		if d.ID == "" {
			return fmt.Errorf("documents[%d].id is required", i)
		}
		if d.Tags == nil {
			return fmt.Errorf("documents[%d].tags is required", i)
		}
	}
	if r.Total < 0 {
		return errors.New("total must not be negative")
	}
	return nil
}

func (r URLIngestRequest) validate() error {
	if utf8.RuneCountInString(r.URL) > MaxURLChars {
		return fmt.Errorf("url is longer than %d characters", MaxURLChars)
	}
	if !urlRe.MatchString(r.URL) {
		return errors.New("url must be an http or https URL")
	}
	return checkTags(r.Tags)
}

func (r URLIngestResponse) validate() error {
	if r.TaskID == "" {
		return errors.New("task_id is required")
	}
	if r.Status == "" {
		return errors.New("status is required")
	}
	return nil
}

func (r TaskStatusRequest) validate() error {
	return checkChars("id", r.ID, 1, MaxTaskIDChars)
}

func (r TaskStatusResponse) validate() error {
	for _, field := range []struct{ name, value string }{{"id", r.ID}, {"kind", r.Kind}, {"status", r.Status}} {
		if field.value == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	if err := checkChars("error", r.Error, 0, MaxTaskErrorChars); err != nil {
		return err
	}
	if !isUTCTimestamp(r.CreatedAt) {
		return errors.New("created_at must be an RFC 3339 time in UTC ending in Z")
	}
	if !isUTCTimestamp(r.UpdatedAt) {
		return errors.New("updated_at must be an RFC 3339 time in UTC ending in Z")
	}
	return nil
}

func checkChars(field, value string, minChars, maxChars int) error {
	n := utf8.RuneCountInString(value)
	if n < minChars || n > maxChars {
		return fmt.Errorf("%s must be %d to %d characters", field, minChars, maxChars)
	}
	return nil
}

func checkTags(tags []string) error {
	if len(tags) > MaxTags {
		return fmt.Errorf("tags has more than %d items", MaxTags)
	}
	for i, tag := range tags {
		if err := checkChars(fmt.Sprintf("tags[%d]", i), tag, 1, MaxTagChars); err != nil {
			return err
		}
	}
	return nil
}

func checkOptionalTimestamp(field, value string) error {
	if value != "" && !isUTCTimestamp(value) {
		return fmt.Errorf("%s must be an RFC 3339 time in UTC ending in Z", field)
	}
	return nil
}

// isUTCTimestamp reports whether s is a real RFC 3339 time that ends in Z.
func isUTCTimestamp(s string) bool {
	if !timestampRe.MatchString(s) {
		return false
	}
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}
