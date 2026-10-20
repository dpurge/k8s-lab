package router

import (
	"context"
	"log/slog"
	"strings"

	"k8s-lab/shared/modelcfg"
)

// Reasons a model is not available, as reported by ModelStatus.Reason, the
// "skipped" list of "llm candidates" and the text of a NoModelError.
const (
	// ReasonCooldown: the circuit breaker holds the model or its provider.
	ReasonCooldown = "cooldown"
	// ReasonMissingCredentials: the provider needs a credential that is not
	// present (see modelcfg.ResolveCredential).
	ReasonMissingCredentials = "missing_credentials"
)

// WithAvailability sets a function that says whether a provider can be used
// right now: "" when it can, else a short reason code such as
// ReasonMissingCredentials. The router asks once per provider on every Select
// and skips the models of an unusable provider, like ones the breaker holds.
// Each change of a provider's usability is logged once; see the package
// documentation.
func WithAvailability(f func(modelcfg.Provider) string) Option {
	return func(r *Router) { r.availability = f }
}

// CredentialAvailability is the availability function for the credentials of
// models.yaml: a provider that requires a credential and has none present, or
// that has an env: or file: header without a value, is unusable with
// ReasonMissingCredentials. A nil getenv or readFile means
// os.Getenv or os.ReadFile.
func CredentialAvailability(getenv func(string) string, readFile func(string) ([]byte, error)) func(modelcfg.Provider) string {
	return func(p modelcfg.Provider) string {
		if modelcfg.CredentialUsable(p, getenv, readFile) {
			return ""
		}
		return ReasonMissingCredentials
	}
}

// providerReasons evaluates the availability function for every provider and
// returns the reason of each unusable one. It logs the providers whose usability
// changed since the last evaluation.
func (r *Router) providerReasons() map[string]string {
	if r.availability == nil {
		return nil
	}
	now := make(map[string]string)
	for _, p := range r.cfg.Providers {
		if reason := r.availability(p); reason != "" {
			now[p.ID] = reason
		}
	}

	type change struct {
		provider modelcfg.Provider
		reason   string // "" on recovery
	}
	var changes []change
	r.unusableMu.Lock()
	for _, p := range r.cfg.Providers {
		was, is := r.unusable[p.ID], now[p.ID]
		switch {
		case was == "" && is != "":
			changes = append(changes, change{p, is})
		case was != "" && is == "":
			changes = append(changes, change{p, ""})
		}
	}
	r.unusable = now
	r.unusableMu.Unlock()

	for _, c := range changes {
		r.logProviderChange(c.provider, c.reason)
	}
	return now
}

// logProviderChange writes the line for a provider that became unusable (reason
// set) or usable again. The credential source is a variable name or file path,
// followed by "header NAME from SOURCE" for each header read from the
// environment or a file; no value is ever available here.
func (r *Router) logProviderChange(p modelcfg.Provider, reason string) {
	var models []string
	for _, m := range r.cfg.Models {
		if m.Provider == p.ID {
			models = append(models, m.ID)
		}
	}
	if len(models) == 0 {
		return
	}
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	source := p.APIKeyFrom
	if source == "" {
		source = "none"
	}
	if headers := modelcfg.HeaderSources(p); len(headers) > 0 {
		source += "; " + strings.Join(headers, "; ")
	}
	if reason == "" {
		logger.LogAttrs(context.Background(), slog.LevelInfo, msgProviderAvailable,
			slog.String("provider_id", p.ID), slog.Any("models", models), slog.String("credential_source", source))
		return
	}
	logger.LogAttrs(context.Background(), slog.LevelWarn, msgProviderUnavailable,
		slog.String("provider_id", p.ID), slog.String("reason", reason),
		slog.Any("models", models), slog.String("credential_source", source))
}
