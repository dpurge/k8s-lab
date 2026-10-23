package bus

import "k8s-lab/shared/message"

// RequestSubject returns the subject an app listens on for requests,
// dp.<app>.request. It returns "" when app is not a valid app name (1 to 64
// characters of a-z, 0-9, _ and -, the rule of message.Envelope's from and to),
// and "" is not a subject NATS accepts, so a request to it fails loudly.
func RequestSubject(app string) string {
	if !validApp(app) {
		return ""
	}
	return "dp." + app + ".request"
}

// validApp applies the app name rule of package message by asking message
// itself, so the two cannot drift apart: an envelope from and to that app is
// valid exactly when the name is.
func validApp(app string) bool {
	env, err := message.NewRequest(app, app, "ping", nil)
	return err == nil && message.Validate(env, nil) == nil
}
