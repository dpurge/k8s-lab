package yamlschema

import (
	"errors"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const testSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["name", "items"],
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "count": {"type": "integer", "minimum": 0},
    "items": {"type": "array", "items": {"$ref": "#/$defs/item"}}
  },
  "$defs": {
    "item": {"type": "object", "additionalProperties": false, "required": ["id"], "properties": {"id": {"type": "string"}, "n": {"type": "integer"}}}
  }
}`

func compile(t *testing.T) *Schema {
	t.Helper()
	s, err := Compile([]byte(testSchema), "test.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func findingsOf(t *testing.T, data string) *Error {
	t.Helper()
	_, err := compile(t).Check("f.yaml", []byte(data))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want *Error, got %T %v", err, err)
	}
	return e
}

func find(t *testing.T, e *Error, path string) Finding {
	t.Helper()
	for _, f := range e.Findings {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no finding for %q in:\n%s", path, e)
	return Finding{}
}

func TestValidFileReturnsTheDocument(t *testing.T) {
	root, err := compile(t).Check("f.yaml", []byte("name: a\nitems:\n  - id: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name string `yaml:"name"`
	}
	if err := root.Decode(&got); err != nil || got.Name != "a" {
		t.Errorf("decode: %+v %v", got, err)
	}
}

func TestCompileRejectsABrokenSchema(t *testing.T) {
	if _, err := Compile([]byte(`{"type": "banana"}`), "bad.json"); err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("err = %v", err)
	}
	if _, err := Compile([]byte(`{not json`), "worse.json"); err == nil {
		t.Errorf("unparseable schema must be an error")
	}
}

func TestSyntaxErrorsEmptyFilesAndNonStringKeys(t *testing.T) {
	if e := findingsOf(t, "name: [unclosed\n"); e.Findings[0].Path != "(file)" || e.Findings[0].Line < 1 {
		t.Errorf("syntax: %+v", e.Findings)
	}
	for _, in := range []string{"", "  \n", "# only a comment\n"} {
		if e := findingsOf(t, in); !strings.Contains(e.Findings[0].Message, "empty") {
			t.Errorf("%q: %+v", in, e.Findings)
		}
	}
	if e := findingsOf(t, "1: one\n"); len(e.Findings) == 0 {
		t.Errorf("a file of non-string keys must produce findings")
	}
}

func TestFindingsNameKeyAndLine(t *testing.T) {
	e := findingsOf(t, "name: a\ncount: -1\nitems:\n  - id: x\n    bogus: 1\n  - n: 2\nextra: true\n")
	if f := find(t, e, "count"); f.Line != 2 {
		t.Errorf("count line = %d", f.Line)
	}
	if f := find(t, e, "items.0.bogus"); f.Line != 5 || !strings.Contains(f.Message, `unknown key "bogus"`) {
		t.Errorf("bogus = %+v", f)
	}
	if f := find(t, e, "items.1"); f.Line != 6 || !strings.Contains(f.Message, `missing required key "id"`) {
		t.Errorf("items.1 = %+v", f)
	}
	if f := find(t, e, "extra"); f.Line != 7 {
		t.Errorf("extra line = %d", f.Line)
	}
	if !strings.HasPrefix(e.Error(), "f.yaml:2: count:") {
		t.Errorf("Error() = %q", e.Error())
	}
}

func TestMissingRequiredKeysAtTheRoot(t *testing.T) {
	e := findingsOf(t, "count: 1\n")
	msgs := e.Error()
	for _, key := range []string{"name", "items"} {
		if !strings.Contains(msgs, `missing required key "`+key+`"`) {
			t.Errorf("no finding for %s:\n%s", key, msgs)
		}
	}
	if find(t, e, "(root)").Line != 1 {
		t.Errorf("root findings sit on line 1")
	}
}

func TestFindingsAreOrderedByLineAndNotRepeated(t *testing.T) {
	e := findingsOf(t, "name: 5\ncount: x\nitems: []\nzzz: 1\n")
	for i := 1; i < len(e.Findings); i++ {
		if e.Findings[i].Line < e.Findings[i-1].Line {
			t.Errorf("not in line order:\n%s", e)
		}
	}
	seen := map[Finding]bool{}
	for _, f := range e.Findings {
		if seen[f] {
			t.Errorf("repeated finding %+v", f)
		}
		seen[f] = true
	}
}

func TestLineOfAndFormatPath(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("a:\n  b:\n    - x\n    - y: 1\nc: 2\n"), &root); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		segments []string
		line     int
	}{
		{nil, 1}, {[]string{"a"}, 1}, {[]string{"a", "b"}, 2}, {[]string{"a", "b", "1"}, 4}, {[]string{"a", "b", "1", "y"}, 4},
		{[]string{"c"}, 5}, {[]string{"a", "nope"}, 1}, {[]string{"a", "b", "9"}, 2}, {[]string{"missing", "deeper"}, 1},
	}
	for _, c := range cases {
		if got := LineOf(&root, c.segments); got != c.line {
			t.Errorf("LineOf(%v) = %d, want %d", c.segments, got, c.line)
		}
	}
	if FormatPath(nil) != "(root)" || FormatPath([]string{"a", "0", "b"}) != "a.0.b" {
		t.Errorf("FormatPath wrong")
	}
}

func TestACheckerIsReusableAndConcurrentSafe(t *testing.T) {
	s := compile(t)
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := s.Check("f.yaml", []byte("name: a\nitems: []\n"))
			done <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}
