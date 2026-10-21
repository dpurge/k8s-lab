package server

import (
	"strings"

	"phraseforge/internal/i18n"
)

// usageAppI18nKeys are the strings usage-app.js uses: the models, performance,
// attempts and purge strings that used to belong to the Admin LLM tab, and the
// Usage section's own. The names of the LLM kinds are the Admin tab's.
var usageAppI18nKeys = append([]string{
	"admin.llm_models_heading", "admin.llm_models_refresh", "admin.llm_col_model", "admin.llm_col_provider",
	"admin.llm_col_priority", "admin.llm_col_availability", "admin.llm_available", "admin.llm_unavailable",
	"admin.llm_reason_cooldown", "admin.llm_reason_no_credentials",
	"admin.llm_models_none", "admin.llm_perf_heading", "admin.llm_perf_period", "admin.llm_perf_group",
	"admin.llm_perf_hint", "admin.llm_perf_none", "admin.llm_window_1h", "admin.llm_window_24h",
	"admin.llm_window_7d", "admin.llm_window_30d", "admin.llm_window_all", "admin.llm_group_model",
	"admin.llm_group_kind_model", "admin.llm_col_purpose", "admin.llm_col_calls", "admin.llm_col_success",
	"admin.llm_col_tokens_per_second", "admin.llm_col_cost", "admin.llm_short_calls", "admin.llm_short_success",
	"admin.llm_short_time", "admin.llm_short_tokens", "admin.llm_short_tokens_per_second", "admin.llm_short_cost",
	"admin.llm_col_time_stats", "admin.llm_attempt_request", "admin.llm_outcome_ok",
	"admin.llm_outcome_error", "admin.llm_outcome_timeout", "admin.llm_outcome_rate_limited", "admin.llm_outcome_canceled",
	"admin.llm_attempts_heading", "admin.llm_attempts_filter_all", "admin.llm_attempts_none", "admin.llm_col_time",
	"admin.llm_col_outcome", "admin.llm_col_duration", "admin.llm_col_tokens", "admin.llm_col_http",
	"admin.llm_purge_heading", "admin.llm_purge_label",
	"admin.llm_purge_option_7", "admin.llm_purge_option_30", "admin.llm_purge_option_90", "admin.llm_purge_option_all",
	"admin.llm_purge_button", "admin.llm_purge_confirm_days", "admin.llm_purge_confirm_all", "admin.llm_purge_confirm_button",
	"admin.llm_purge_cancel", "admin.llm_purge_done", "admin.llm_purge_hint",
	"admin.llm_model_tags",
	"usage.title", "usage.page_usage", "usage.page_models", "usage.page_performance", "usage.page_attempts",
	"usage.col_user", "usage.col_calls", "usage.col_input", "usage.col_output", "usage.col_cost", "usage.col_duration",
	"usage.group_by", "usage.group_all", "usage.group_user", "usage.group_provider", "usage.group_model",
	"usage.empty", "usage.summary", "usage.attempts_total",
	"texts.pagination_previous", "texts.pagination_next",
}, llmKindI18nKeys()...)

func llmKindI18nKeys() []string {
	var out []string
	for _, k := range adminAppI18nKeys {
		if strings.HasPrefix(k, "admin.llm_kind_") {
			out = append(out, k)
		}
	}
	return out
}

func usageAppI18n(loc string) map[string]string {
	out := make(map[string]string, len(usageAppI18nKeys))
	for _, k := range usageAppI18nKeys {
		out[k] = i18n.T(loc, k)
	}
	return out
}
