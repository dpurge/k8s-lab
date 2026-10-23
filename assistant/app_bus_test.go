package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"assistant/internal/metrics"
)

// busTestApp is an app with only what connectBus uses, so no database is needed.
func busTestApp() (*app, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	return &app{metrics: metrics.New(), log: slog.New(slog.NewJSONHandler(logs, nil))}, logs
}

func TestNoNATSURLMeansNoClientAndNoLog(t *testing.T) {
	for _, url := range []string{"", "  \n"} {
		a, logs := busTestApp()
		a.connectBus(url)
		if a.bus != nil {
			t.Errorf("url %q: a client was built", url)
		}
		if logs.Len() != 0 {
			t.Errorf("url %q: logged %s", url, logs)
		}
		a.closeBus() // nothing to close; must not panic
	}
}

func TestAFailedNATSConnectionWarnsWithoutTheURLAndContinues(t *testing.T) {
	a, logs := busTestApp()
	// Nothing listens on port 1, so the connection is refused at once.
	a.connectBus("nats://user:s3cr3t-pass@127.0.0.1:1")
	if a.bus != nil {
		t.Fatal("a client was built for a server that is not there")
	}
	out := logs.String()
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "could not connect to NATS") {
		t.Errorf("no warning in the log: %s", out)
	}
	if strings.Contains(out, "s3cr3t-pass") || strings.Contains(out, "127.0.0.1") {
		t.Errorf("the log holds the URL: %s", out)
	}
	a.closeBus()
}
