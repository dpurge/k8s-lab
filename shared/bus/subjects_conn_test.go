package bus

import (
	"strings"
	"testing"

	"k8s-lab/shared/message"
)

func TestRequestSubject(t *testing.T) {
	tests := []struct {
		app  string
		want string
	}{
		{"assistant", "dp.assistant.request"},
		{"knowledge", "dp.knowledge.request"},
		{"app_1-b", "dp.app_1-b.request"},
		{strings.Repeat("a", 64), "dp." + strings.Repeat("a", 64) + ".request"},
		{"", ""},
		{"Assistant", ""},
		{"a.b", ""},
		{"a b", ""},
		{"a*", ""},
		{"a>", ""},
		{"a\nb", ""},
		{"é", ""},
		{strings.Repeat("a", 65), ""},
	}
	for _, tt := range tests {
		if got := RequestSubject(tt.app); got != tt.want {
			t.Errorf("RequestSubject(%q) = %q, want %q", tt.app, got, tt.want)
		}
	}
}

// TestAppNameRuleIsMessages proves the rule is the one of message.Envelope: the
// same names are valid as a from or to.
func TestAppNameRuleIsMessages(t *testing.T) {
	for _, app := range []string{"a", "a-b_c9", "-", "_", "0", "A", "a.b", "a b", "", strings.Repeat("z", 64), strings.Repeat("z", 65), "é", "a/b"} {
		req, err := message.NewRequest(app, "knowledge", "ping", nil)
		if err != nil {
			t.Fatal(err)
		}
		want := message.Validate(req, nil) == nil
		if got := validApp(app); got != want {
			t.Errorf("validApp(%q) = %v, message says %v", app, got, want)
		}
	}
}

func TestEnabled(t *testing.T) {
	tests := map[string]bool{
		"":                      false,
		"   ":                   false,
		"\t\n":                  false,
		"nats://localhost:4222": true,
		"  nats://x:4222 ":      true,
	}
	for url, want := range tests {
		if got := Enabled(url); got != want {
			t.Errorf("Enabled(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestConnectWithoutURLFails(t *testing.T) {
	nc, err := Connect(Config{URL: "  ", Name: "x"})
	if err == nil || nc != nil {
		t.Fatalf("want an error and no connection, got %v %v", nc, err)
	}
}

func TestConnectFailureNamesNoDevCommandAndNoURL(t *testing.T) {
	// Nothing listens on port 1, so the dial is refused at once.
	nc, err := Connect(Config{URL: "nats://user:s3cret@127.0.0.1:1", Name: "x"})
	if err == nil {
		Close(nc)
		t.Fatal("want an error")
	}
	for _, forbidden := range []string{"task ", "s3cret", "127.0.0.1"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("the error %q holds %q", err, forbidden)
		}
	}
}

func TestCloseNilIsFine(t *testing.T) {
	if err := Close(nil); err != nil {
		t.Fatal(err)
	}
}
