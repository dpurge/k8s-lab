package message

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The examples in doc.go are the reference people read; they must be the
// envelopes the tests use, so they cannot drift from the format.
func TestDocExamplesAreTheTestdataEnvelopes(t *testing.T) {
	source, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatal(err)
	}
	bare := regexp.MustCompile(`(?m)^//`).ReplaceAllString(string(source), "")
	bare = strings.Join(strings.Fields(bare), "")
	for _, file := range []string{"request_documents_search.json", "response_documents_search.json"} {
		want := strings.Join(strings.Fields(string(readTestdata(t, file))), "")
		if !strings.Contains(bare, want) {
			t.Errorf("doc.go does not show %s", file)
		}
	}
}
