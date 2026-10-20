package router

// HasAllTags reports whether modelTags contains every one of wanted; no wanted
// tags matches every model. It is the one definition of "a model has the tags"
// that selection, validation and the admin API share.
func HasAllTags(modelTags, wanted []string) bool {
	for _, w := range wanted {
		found := false
		for _, h := range modelTags {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
