package ops

import (
	"encoding/json"
	"testing"
)

// side is one payload of one operation: its schema, the Parse function that
// stands for the Go types, and the samples both must judge alike.
type side struct {
	operation string
	side      string
	parse     func(json.RawMessage) error
	samples   []sample
}

type sample struct {
	name  string
	json  json.RawMessage
	valid bool
}

func ok(name string, v any) sample  { return sample{name: name, json: mustJSONNoT(v), valid: true} }
func bad(name string, v any) sample { return sample{name: name, json: mustJSONNoT(v), valid: false} }

func mustJSONNoT(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func raw(name, text string, valid bool) sample {
	return sample{name: name, json: json.RawMessage(text), valid: valid}
}

// verdictSides lists the samples. Left out on purpose, because Go reads an
// optional field left out and one holding its zero value alike, and a JSON null
// as an absent value, while the schema tells them apart: "limit": 0, "start":
// "", "tags": null, and 5.0 as an integer, and a document left without title,
// summary, url or score (Go sees an empty string or 0). A nonexistent date such as month 13
// is also left out: the schema checks the shape of a time, Go checks that it is
// real. The Go-only tests cover those.
func verdictSides() []side {
	parseSearchRequest := func(p json.RawMessage) error { _, err := ParseDocumentsSearchRequest(p); return err }
	parseSearchResponse := func(p json.RawMessage) error { _, err := ParseDocumentsSearchResponse(p); return err }
	parseIngestRequest := func(p json.RawMessage) error { _, err := ParseURLIngestRequest(p); return err }
	parseIngestResponse := func(p json.RawMessage) error { _, err := ParseURLIngestResponse(p); return err }
	parseStatusRequest := func(p json.RawMessage) error { _, err := ParseTaskStatusRequest(p); return err }
	parseStatusResponse := func(p json.RawMessage) error { _, err := ParseTaskStatusResponse(p); return err }

	return []side{
		{DocumentsSearch, "request", parseSearchRequest, []sample{
			ok("minimal", baseSearchRequest),
			ok("everything", with(baseSearchRequest, object{"tags": []any{"ops", "k8s"}, "start": "2026-01-01T00:00:00Z", "end": "2026-10-23T09:15:00.250Z", "limit": 20, "offset": 40})),
			bad("query missing", with(baseSearchRequest, object{"query": nil})),
			bad("query empty", with(baseSearchRequest, object{"query": ""})),
			ok("query 2000 characters", with(baseSearchRequest, object{"query": repeat("q", 2000)})),
			bad("query 2001 characters", with(baseSearchRequest, object{"query": repeat("q", 2001)})),
			ok("query 2000 multibyte characters", with(baseSearchRequest, object{"query": repeat("ż", 2000)})),
			bad("query 2001 multibyte characters", with(baseSearchRequest, object{"query": repeat("ż", 2001)})),
			bad("query a number", with(baseSearchRequest, object{"query": 5})),
			ok("tags empty list", with(baseSearchRequest, object{"tags": []any{}})),
			ok("20 tags", with(baseSearchRequest, object{"tags": tagList(20)})),
			bad("21 tags", with(baseSearchRequest, object{"tags": tagList(21)})),
			ok("tag of 64 characters", with(baseSearchRequest, object{"tags": []any{repeat("t", 64)}})),
			bad("tag of 65 characters", with(baseSearchRequest, object{"tags": []any{repeat("t", 65)}})),
			bad("empty tag", with(baseSearchRequest, object{"tags": []any{""}})),
			bad("tag a number", with(baseSearchRequest, object{"tags": []any{1}})),
			bad("tags a string", with(baseSearchRequest, object{"tags": "ops"})),
			ok("start with fraction", with(baseSearchRequest, object{"start": "2026-10-23T09:15:00.123Z"})),
			bad("start with an offset", with(baseSearchRequest, object{"start": "2026-10-23T09:15:00+02:00"})),
			bad("start a date only", with(baseSearchRequest, object{"start": "2026-10-23"})),
			bad("end lower-case z", with(baseSearchRequest, object{"end": "2026-10-23T09:15:00z"})),
			ok("limit 1", with(baseSearchRequest, object{"limit": 1})),
			ok("limit 20", with(baseSearchRequest, object{"limit": 20})),
			bad("limit 21", with(baseSearchRequest, object{"limit": 21})),
			bad("limit -1", with(baseSearchRequest, object{"limit": -1})),
			bad("limit fraction", with(baseSearchRequest, object{"limit": 2.5})),
			ok("offset 0", with(baseSearchRequest, object{"offset": 0})),
			ok("offset large", with(baseSearchRequest, object{"offset": 1000000})),
			bad("offset -1", with(baseSearchRequest, object{"offset": -1})),
			bad("unknown field", with(baseSearchRequest, object{"page": 2})),
			bad("empty object", object{}),
			raw("not an object", `[]`, false),
		}},
		{DocumentsSearch, "response", parseSearchResponse, []sample{
			ok("one document", baseSearchResponse),
			ok("no documents", object{"documents": []any{}}),
			ok("with total", with(baseSearchResponse, object{"total": 133})),
			ok("total 0", with(baseSearchResponse, object{"total": 0})),
			bad("total -1", with(baseSearchResponse, object{"total": -1})),
			ok("20 documents", object{"documents": documents(20)}),
			bad("21 documents", object{"documents": documents(21)}),
			bad("documents missing", object{}),
			bad("document without tags", object{"documents": []any{with(baseDocument, object{"tags": nil})}}),
			bad("document with empty id", object{"documents": []any{with(baseDocument, object{"id": ""})}}),
			bad("document with extra field", object{"documents": []any{with(baseDocument, object{"text": "full text"})}}),
			bad("document score a string", object{"documents": []any{with(baseDocument, object{"score": "high"})}}),
			ok("document score integer", object{"documents": []any{with(baseDocument, object{"score": 1})}}),
			ok("document with relative url", object{"documents": []any{with(baseDocument, object{"url": "../documents/d-17"})}}),
			bad("unknown field", with(baseSearchResponse, object{"next": "x"})),
		}},
		{URLIngest, "request", parseIngestRequest, []sample{
			ok("minimal", baseIngestRequest),
			ok("http", with(baseIngestRequest, object{"url": "http://example.org/a?b=c#d"})),
			ok("with tags", with(baseIngestRequest, object{"tags": []any{"ops"}})),
			bad("url missing", with(baseIngestRequest, object{"url": nil})),
			bad("url empty", with(baseIngestRequest, object{"url": ""})),
			bad("ftp", with(baseIngestRequest, object{"url": "ftp://example.org/a"})),
			bad("file", with(baseIngestRequest, object{"url": "file:///etc/passwd"})),
			bad("no scheme", with(baseIngestRequest, object{"url": "example.org/a"})),
			bad("scheme only", with(baseIngestRequest, object{"url": "https://"})),
			bad("space in url", with(baseIngestRequest, object{"url": "https://example.org/a b"})),
			ok("url of 2048 characters", with(baseIngestRequest, object{"url": "https://e.org/" + repeat("a", 2048-14)})),
			bad("url of 2049 characters", with(baseIngestRequest, object{"url": "https://e.org/" + repeat("a", 2049-14)})),
			bad("21 tags", with(baseIngestRequest, object{"tags": tagList(21)})),
			bad("tag of 65 characters", with(baseIngestRequest, object{"tags": []any{repeat("t", 65)}})),
			bad("unknown field", with(baseIngestRequest, object{"depth": 2})),
		}},
		{URLIngest, "response", parseIngestResponse, []sample{
			ok("queued", baseIngestResponse),
			bad("task_id missing", with(baseIngestResponse, object{"task_id": nil})),
			bad("task_id empty", with(baseIngestResponse, object{"task_id": ""})),
			bad("status missing", with(baseIngestResponse, object{"status": nil})),
			bad("status empty", with(baseIngestResponse, object{"status": ""})),
			bad("status a number", with(baseIngestResponse, object{"status": 1})),
			bad("unknown field", with(baseIngestResponse, object{"result": "x"})),
		}},
		{TaskStatus, "request", parseStatusRequest, []sample{
			ok("id", baseStatusRequest),
			bad("id missing", object{}),
			bad("id empty", object{"id": ""}),
			ok("id of 128 characters", object{"id": repeat("i", 128)}),
			bad("id of 129 characters", object{"id": repeat("i", 129)}),
			ok("id of 128 multibyte characters", object{"id": repeat("ż", 128)}),
			bad("id a number", object{"id": 7}),
			bad("unknown field", with(baseStatusRequest, object{"app": "knowledge"})),
		}},
		{TaskStatus, "response", parseStatusResponse, []sample{
			ok("minimal", baseStatusResponse),
			ok("everything", with(baseStatusResponse, object{"step": "fetch", "progress": "3/10", "error": "fetch failed"})),
			bad("id missing", with(baseStatusResponse, object{"id": nil})),
			bad("kind missing", with(baseStatusResponse, object{"kind": nil})),
			bad("kind empty", with(baseStatusResponse, object{"kind": ""})),
			bad("status missing", with(baseStatusResponse, object{"status": nil})),
			bad("created_at missing", with(baseStatusResponse, object{"created_at": nil})),
			bad("updated_at missing", with(baseStatusResponse, object{"updated_at": nil})),
			bad("created_at with an offset", with(baseStatusResponse, object{"created_at": "2026-10-23T09:15:00+01:00"})),
			bad("updated_at not a time", with(baseStatusResponse, object{"updated_at": "yesterday"})),
			ok("error of 1000 characters", with(baseStatusResponse, object{"error": repeat("e", 1000)})),
			bad("error of 1001 characters", with(baseStatusResponse, object{"error": repeat("e", 1001)})),
			ok("error of 1000 multibyte characters", with(baseStatusResponse, object{"error": repeat("ż", 1000)})),
			bad("error of 1001 multibyte characters", with(baseStatusResponse, object{"error": repeat("ż", 1001)})),
			bad("payload", with(baseStatusResponse, object{"payload": object{"url": "https://x.org"}})),
			bad("result", with(baseStatusResponse, object{"result": "text"})),
			bad("progress a number", with(baseStatusResponse, object{"progress": 0.5})),
		}},
	}
}

func TestGoTypesAndTheJSONSchemasAgree(t *testing.T) {
	total := 0
	for _, s := range verdictSides() {
		req, resp, found := Schemas(s.operation)
		if !found {
			t.Fatalf("no schemas for %s", s.operation)
		}
		schemaJSON := req
		if s.side == "response" {
			schemaJSON = resp
		}
		schema := compileSchema(t, s.operation+"."+s.side, schemaJSON)
		for _, c := range s.samples {
			total++
			t.Run(s.operation+" "+s.side+": "+c.name, func(t *testing.T) {
				goErr := s.parse(c.json)
				schemaOK := schemaAccepts(t, schema, c.json)
				if (goErr == nil) != c.valid {
					t.Errorf("Go: valid=%v want %v (err %v) for %s", goErr == nil, c.valid, goErr, c.json)
				}
				if schemaOK != c.valid {
					t.Errorf("schema: valid=%v want %v for %s", schemaOK, c.valid, c.json)
				}
			})
		}
	}
	if total < 30 {
		t.Fatalf("only %d samples", total)
	}
	t.Logf("%d samples", total)
}
