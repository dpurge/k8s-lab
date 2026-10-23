package bus

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	connectTimeout = 5 * time.Second
	reconnectWait  = 2 * time.Second
	drainTimeout   = 5 * time.Second
)

// Config says how an app connects to NATS.
type Config struct {
	// URL of the server, as in nats://host:4222; several URLs may be separated by commas.
	URL string
	// Name is the client name the server shows, usually the app name.
	Name string
	// Logger receives the connection events; nil means slog.Default().
	Logger *slog.Logger
}

// Enabled reports whether a NATS URL is configured. Apps listen and send only
// when it is: the bus is off by default.
func Enabled(url string) bool {
	return strings.TrimSpace(url) != ""
}

// Connect opens the connection. It fails when no server answers within 5 s;
// once connected it keeps reconnecting without limit, 2 s apart, and logs the
// disconnects, reconnects and the close (never a message). Close it with Close.
func Connect(cfg Config) (*nats.Conn, error) {
	if !Enabled(cfg.URL) {
		return nil, errors.New("connect to the message bus: no NATS URL is configured")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	nc, err := nats.Connect(strings.TrimSpace(cfg.URL),
		nats.Name(cfg.Name),
		nats.Timeout(connectTimeout),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(reconnectWait),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.Warn("bus disconnected", "name", cfg.Name, "error", errText(err))
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("bus reconnected", "name", cfg.Name, "server", nc.ConnectedUrlRedacted())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			logger.Info("bus closed", "name", cfg.Name)
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			subject := ""
			if sub != nil {
				subject = sub.Subject
			}
			logger.Warn("bus error", "name", cfg.Name, "subject", subject, "error", errText(err))
		}),
	)
	if err != nil {
		// The error is the client's own text; it names no URL, so no credentials.
		return nil, fmt.Errorf("connect to the message bus (is the NATS server reachable and its URL right?): %w", err)
	}
	return nc, nil
}

// Close drains the connection (messages already received are handled and the
// pending ones sent) and waits for it to close, for at most 5 s; then it closes
// it. A nil connection is fine. The error says that the drain did not finish.
func Close(nc *nats.Conn) error {
	if nc == nil || nc.IsClosed() {
		return nil
	}
	if err := nc.Drain(); err != nil {
		nc.Close()
		return fmt.Errorf("drain the message bus connection: %w", err)
	}
	deadline := time.Now().Add(drainTimeout)
	for !nc.IsClosed() {
		if time.Now().After(deadline) {
			nc.Close()
			return errors.New("drain the message bus connection: it did not finish within 5 s and was closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// errText returns an error's text, or "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
