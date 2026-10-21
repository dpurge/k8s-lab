package server

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"phraseforge/internal/i18n"
)

// usageKeyUse matches every string in usage-app.js shaped like a translation
// key: the ones passed to T() and the ones kept in its lookup tables.
var usageKeyUse = regexp.MustCompile(`"((?:admin|usage|texts)\.[a-z0-9_]+)"`)

// TestEveryUsageAppKeyIsServedAndTranslated: the Usage section only receives the
// keys listed in usageAppI18nKeys, so one used in usage-app.js but missing there
// shows as the raw key.
func TestEveryUsageAppKeyIsServedAndTranslated(t *testing.T) {
	js, err := staticFS.ReadFile("static/js/usage-app.js")
	if err != nil {
		t.Fatal(err)
	}
	uses := usageKeyUse.FindAllStringSubmatch(string(js), -1)
	if len(uses) < 20 {
		t.Fatalf("found %d keys in usage-app.js; the pattern is stale", len(uses))
	}
	keys := map[string]bool{}
	for _, use := range uses {
		if strings.HasSuffix(use[1], "_") {
			continue // a prefix the script completes with a page or a kind
		}
		keys[use[1]] = true
	}
	// Keys built from a page name or a kind: T("usage.page_" + id).
	for _, page := range []string{"usage", "models", "performance", "attempts"} {
		keys["usage.page_"+page] = true
	}
	for key := range keys {
		if !slices.Contains(usageAppI18nKeys, key) {
			t.Errorf("usage-app.js uses %s, missing from usageAppI18nKeys", key)
		}
	}
	// Everything served is translated in every locale, used or not.
	for _, key := range usageAppI18nKeys {
		for _, l := range i18n.Locales {
			if !i18n.Has(l.Code, key) {
				t.Errorf("%s not translated in locale %q", key, l.Code)
			}
		}
	}
	// The LLM kinds' names come with the section: the performance table's kind
	// column shows them.
	if !slices.Contains(usageAppI18nKeys, "admin.llm_kind_translation") {
		t.Error("the LLM kind names are not served to the Usage section")
	}
}
