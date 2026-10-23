package responder

import (
	"context"
	"errors"
	"log/slog"
	"time"
	"unicode/utf8"

	"k8s-lab/shared/bus"
	"k8s-lab/shared/message"
	"k8s-lab/shared/ops"
	"knowledge/internal/ingest"
	"knowledge/internal/jobs"
	"knowledge/internal/qdrant"
)

// defaultLimit is the page size of documents_search when the request leaves
// limit out (ops.DocumentsSearchRequest).
const defaultLimit = 5

// Fixed texts of the errors a requester sees. They name no value of the request
// and never a Go error: those stay in the log.
const (
	searchUnavailableText = "the search is not available right now; try again later"
	invalidURLText        = "the url is not valid: it must be an http or https URL with a host"
	ingestBusyText        = "another ingest is running; try again later"
	jobNotFoundText       = "no task with that id exists"
)

type handlers struct {
	svc     Services
	observe Observer
	log     *slog.Logger
}

// instrument logs the operation, the message id and the outcome of fn, and
// counts it. It never looks at the payload.
func (h handlers) instrument(operation string, fn bus.Handler) bus.Handler {
	return func(ctx context.Context, req message.Envelope) (any, error) {
		payload, err := fn(ctx, req)
		outcome := outcomeOf(err)
		h.log.Info("bus request handled", "operation", operation, "id", req.ID, "outcome", outcome)
		if h.observe != nil {
			h.observe(operation, outcome)
		}
		return payload, err
	}
}

// outcomeOf is "ok", the code of a *message.Error, or "internal": what
// shared/bus makes of any other error.
func outcomeOf(err error) string {
	var coded *message.Error
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &coded):
		return coded.Code
	}
	return message.CodeInternal
}

// documentsSearch makes the call of the HTTP search handler (Searcher.Search),
// and builds each document as the chat builds its Source.
func (h handlers) documentsSearch(ctx context.Context, req message.Envelope) (any, error) {
	r, err := ops.ParseDocumentsSearchRequest(req.Payload)
	if err != nil {
		return nil, err
	}
	start, err := optionalTime(r.Start)
	if err != nil {
		return nil, err
	}
	end, err := optionalTime(r.End)
	if err != nil {
		return nil, err
	}
	limit := r.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	items, err := h.svc.Search.Search(ctx, qdrant.Search{Query: r.Query, Tags: r.Tags, Start: start, End: end, Limit: limit, Offset: r.Offset})
	if err != nil {
		return nil, &message.Error{Code: message.CodeUnavailable, Message: searchUnavailableText}
	}
	documents := make([]ops.Document, 0, len(items))
	for _, it := range items {
		score := 0.0
		if it.Score != nil {
			score = *it.Score
		}
		tags := it.Tags
		if tags == nil {
			tags = []string{}
		}
		documents = append(documents, ops.Document{ID: it.ID, Title: it.Title, Summary: it.Summary, Tags: tags, Score: score, URL: "/?knowledge=" + it.ID})
	}
	// Total stays 0 (left out): the knowledge base does not count matches.
	return ops.DocumentsSearchResponse{Documents: documents}, nil
}

// urlIngest starts the ingest the HTTP ingest/url handler starts, with the same
// function (Ingester.StartURL, which validates the scheme and host) and the same
// tag normalization. The job is saved before it answers.
func (h handlers) urlIngest(ctx context.Context, req message.Envelope) (any, error) {
	r, err := ops.ParseURLIngestRequest(req.Payload)
	if err != nil {
		return nil, err
	}
	job, err := h.svc.Ingest.StartURL(ctx, r.URL, qdrant.NormalizeTags(r.Tags))
	switch {
	case err == nil:
		return ops.URLIngestResponse{TaskID: job.ID, Status: job.Status}, nil
	case errors.Is(err, ingest.ErrValidation):
		return nil, &message.Error{Code: message.CodeInvalidPayload, Message: invalidURLText}
	case errors.Is(err, ingest.ErrJobRunning):
		return nil, &message.Error{Code: message.CodeUnavailable, Message: ingestBusyText}
	}
	return nil, err
}

// taskStatus returns the metadata of one job. Job has more than this (source
// text, source ref, tags): none of it is copied.
func (h handlers) taskStatus(ctx context.Context, req message.Envelope) (any, error) {
	r, err := ops.ParseTaskStatusRequest(req.Payload)
	if err != nil {
		return nil, err
	}
	job, err := h.svc.Jobs.Get(ctx, r.ID)
	if errors.Is(err, jobs.ErrNotFound) {
		return nil, &message.Error{Code: message.CodeNotFound, Message: jobNotFoundText}
	}
	if err != nil {
		return nil, err
	}
	return ops.TaskStatusResponse{
		ID: job.ID, Kind: job.Kind, Status: job.Status, Step: job.Step,
		Error:     truncateChars(job.Error, ops.MaxTaskErrorChars),
		CreatedAt: formatTime(job.CreatedAt), UpdatedAt: formatTime(job.UpdatedAt),
	}, nil
}

// optionalTime parses an RFC 3339 time, or returns nil for "". The schema has
// checked the shape, so a failure here is an invalid payload, not a crash.
func optionalTime(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, &message.Error{Code: message.CodeInvalidPayload, Message: "start and end must be RFC 3339 times in UTC ending in Z"}
	}
	return &t, nil
}

// formatTime renders t as the RFC 3339 UTC time the schema requires.
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// truncateChars cuts s to at most n characters, on a character boundary.
func truncateChars(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
