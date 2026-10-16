package workflows

import (
	"sort"
	"strings"
	"testing"
)

func TestBuiltinIDsAreExactlyTheBuiltinsOfARegistryWithAModel(t *testing.T) {
	names := New(&fakeClock{}, WithLLM(&fakeLLM{})).Names()
	sort.Strings(names)
	ids := append([]string(nil), BuiltinIDs...)
	sort.Strings(ids)
	if strings.Join(names, ",") != strings.Join(ids, ",") {
		t.Errorf("built-ins %v, BuiltinIDs %v: a new built-in must be reserved", names, ids)
	}
}
