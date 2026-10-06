package server

import (
	"regexp"
	"slices"
	"testing"

	"phraseforge/internal/i18n"
)

// adminKeyUse matches a literal translation key passed to T() in admin-app.js.
var adminKeyUse = regexp.MustCompile(`\bT\("([^"]+)"\)`)

// TestEveryAdminAppKeyIsServedAndTranslated: the admin page only receives the
// keys listed in adminAppI18nKeys, so a key used in admin-app.js but missing
// there shows as the raw key (as admin.tab_languages once did), and one missing
// in a locale falls back or shows raw.
func TestEveryAdminAppKeyIsServedAndTranslated(t *testing.T) {
	js, err := staticFS.ReadFile("static/js/admin-app.js")
	if err != nil {
		t.Fatal(err)
	}
	uses := adminKeyUse.FindAllStringSubmatch(string(js), -1)
	if len(uses) == 0 {
		t.Fatal("found no T(\"...\") calls in admin-app.js; the pattern is stale")
	}
	for _, use := range uses {
		key := use[1]
		if !slices.Contains(adminAppI18nKeys, key) {
			t.Errorf("admin-app.js uses %s, missing from adminAppI18nKeys", key)
		}
		for _, l := range i18n.Locales {
			if !i18n.Has(l.Code, key) {
				t.Errorf("%s not translated in locale %q", key, l.Code)
			}
		}
	}
}
