package a2ui

// Event is a renderer-to-agent message that passed CheckEvent.
type Event struct {
	// Kind is action, error, callAgentFunction or rendererFunctionResponse.
	Kind string
	// SurfaceID and ComponentID say where an action came from, and SurfaceID
	// where an error did; empty when the message names none.
	SurfaceID   string
	ComponentID string
	// Name is the action's name; UserMessage its human-readable description.
	Name        string
	UserMessage string
	// Body is the value under Kind.
	Body map[string]any
}

// CheckEvent refuses a message from the page that is too large, not JSON, or
// not valid against the protocol's renderer-to-agent schema. The page is
// as untrusted as the agent: it is a browser anyone with a session can drive.
func (v *Validator) CheckEvent(raw []byte) (*Event, error) {
	if len(raw) > v.limits.MaxMessageBytes {
		return nil, refuse("", "the message is %d bytes; the limit is %d", len(raw), v.limits.MaxMessageBytes)
	}
	msg, err := decode(raw)
	if err != nil {
		return nil, refuse("", "%v", err)
	}
	if err := v.event.Validate(msg); err != nil {
		return nil, &ValidationError{Problems: schemaProblems("", err)}
	}
	e := &Event{}
	for _, kind := range EventKinds {
		if body, ok := msg[kind].(map[string]any); ok {
			e.Kind, e.Body = kind, body
		}
	}
	if e.Kind == "" {
		return nil, refuse("", "a message from the page has \"version\": \"v1.0\" and one of action, error, callAgentFunction, rendererFunctionResponse")
	}
	e.SurfaceID, _ = e.Body["surfaceId"].(string)
	e.ComponentID, _ = e.Body["sourceComponentId"].(string)
	e.Name, _ = e.Body["name"].(string)
	e.UserMessage, _ = e.Body["userMessage"].(string)
	return e, nil
}

// CheckAction refuses an action that does not come from a component of a
// surface the conversation has drawn: the page may only report what the user
// did to an interface the assistant showed.
func (s *Surfaces) CheckAction(e *Event) error {
	if e.Kind != "action" {
		return nil
	}
	sf, ok := s.surfaces[e.SurfaceID]
	if !ok {
		return refuse("/action/surfaceId", "there is no surface %q in this conversation", e.SurfaceID)
	}
	if _, ok := sf.components[e.ComponentID]; !ok {
		return refuse("/action/sourceComponentId", "the surface %q has no component %q", e.SurfaceID, e.ComponentID)
	}
	return nil
}
