package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The replies below are what the models sent. The first three come from the
// failed ui-compose runs in production (gemma4-31b-gpu), the fourth from a
// replay on a development Ollama (gemma4:12b without constrained decoding).

// prodExtraBrace has one closing brace too many after the components list;
// Python reports "char 510".
const prodExtraBrace = `{"state_patch":{"request":"show a button with text \"GO!\""},"action":{"type":"ask_user","payload":{"question":"Click the button below.","ui":[{"version":"v1.0","createSurface":{"surfaceId":"compose_ui","catalogId":"https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json","components":[{"id":"root","component":"Card","child":"btn"},{"id":"btn","component":"Button","child":"btn_label","action":{"event":{"name":"go","context":{}}}},{"id":"btn_label","component":"Text","text":"GO!"}]},"dataModel":{}}}}]}}}`

// prodCreateSurfaceClosedEarly is valid JSON: createSurface ends after the
// components, so dataModel is its sibling.
const prodCreateSurfaceClosedEarly = `{"state_patch":{"request":"show a button with text \"GO!\""},"action":{"type":"ask_user","payload":{"question":"Please click the button.","ui":[{"version":"v1.0","createSurface":{"surfaceId":"compose_ui","catalogId":"https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json","components":[{"id":"root","component":"Card","child":"btn"},{"id":"btn","component":"Button","child":"btn_label","action":{"event":{"name":"go","context":{}}}},{"id":"btn_label","component":"Text","text":"GO!"}]},"dataModel":{}}]}}}`

// prodStrayBrace has a stray brace closing the components list; Python reports
// "char 493".
const prodStrayBrace = `{"state_patch":{"request":"show a button with text \"GO!\""},"action":{"type":"ask_user","payload":{"question":"Please click the button.","ui":[{"version":"v1.0","createSurface":{"surfaceId":"compose_ui","catalogId":"https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json","components":[{"id":"root","component":"Card","child":"btn"},{"id":"btn","component":"Button","child":"btn_label","action":{"event":{"name":"go","context":{}}}},{"id":"btn_label","component":"Text","text":"GO!"}},"dataModel":{}}}]}}}`

// devEscapedQuotes has escaped quotes where the keys' quotes belong, and
// brackets that do not match.
const devEscapedQuotes = `{"state_patch":{"request":"show a button with text \"GO!\""},"action":{"type":"ask_user","payload":{"question":"I'm building the interface for you.","ui":[{"version":"v1.0","createSurface":{"surfaceId":"go_btn_surface","catalogId":"https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json","components":[{"id":"root","component":"Card","child":"col1"},{"id":"col1","component":"Column","children":["btn1"]},{"id":"btn1","component":"Button","child":"btn_text","variant\":\"primary\",\"action":{"event":{"name":"click","context":{"clicked":"true"}}}}],"dataModel":{"form":{"clicked":false}}}],"dataModel":{"form":{"clicked":false}}}]}}}`

const resendInstruction = "Send the whole reply again as ONE JSON object with nothing before or after it, and count the closing brackets against the example in the guide."

func invalidJSONRefusal(t *testing.T, text string) string {
	t.Helper()
	_, err := parse(t, "teach", text)
	var rerr *ReplyError
	if !errors.As(err, &rerr) {
		t.Fatalf("want *ReplyError, got %T %v", err, err)
	}
	if !strings.HasPrefix(rerr.Message, "reply is not a valid JSON object: ") {
		t.Fatalf("message %q", rerr.Message)
	}
	return rerr.Message
}

func TestInvalidJSONRefusalSaysWhereAndWhatWasOpen(t *testing.T) {
	for _, tc := range []struct {
		name, reply, offset, original string
	}{
		{"prod extra brace", prodExtraBrace, "(at byte 510)", "invalid character '}' after array element"},
		{"prod stray brace", prodStrayBrace, "(at byte 493)", "invalid character '}' after array element"},
		{"dev escaped quotes", devEscapedQuotes, "(at byte ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := invalidJSONRefusal(t, tc.reply)
			t.Log(msg)
			for _, want := range []string{tc.offset, tc.original, "<<HERE>>", "Around it: ", "Open at that point: reply > action > payload > ui[0]", resendInstruction} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q: %s", want, msg)
				}
			}
			if strings.ContainsAny(msg, "\n\t") {
				t.Errorf("message must be one line: %q", msg)
			}
			if len(msg) > 700 {
				t.Errorf("message is %d bytes, want at most 700", len(msg))
			}
		})
	}
}

func TestInvalidJSONRefusalPointsAtTheOffendingByte(t *testing.T) {
	const text = `{"state_patch":{},"action":{"type":"goto","state":["a",,"b"]}}`
	msg := invalidJSONRefusal(t, text)
	if !strings.Contains(msg, `"state":["a",<<HERE>>,"b"]`) {
		t.Errorf("marker misplaced: %s", msg)
	}
	want := fmt.Sprintf("(at byte %d)", strings.Index(text, ",,")+1)
	if !strings.Contains(msg, want) {
		t.Errorf("want %s: %s", want, msg)
	}
	if !strings.Contains(msg, "Open at that point: reply > action > state[1]. ") {
		t.Errorf("path: %s", msg)
	}
}

func TestInvalidJSONRefusalIgnoresBracketsAndQuotesInsideStrings(t *testing.T) {
	const text = `{"state_patch":{"note":"a \"}\" ] { [ b"},"action":{"type":"goto",}}`
	msg := invalidJSONRefusal(t, text)
	if !strings.Contains(msg, "Open at that point: reply > action. ") {
		t.Errorf("path: %s", msg)
	}
}

func TestInvalidJSONRefusalSurvivesLineBreaksAndMultibyteText(t *testing.T) {
	text := "{\"state_patch\":{\"w\":\"" + strings.Repeat("é世", 30) + "\"},\n\t\"action\":{\"type\":}}"
	msg := invalidJSONRefusal(t, text)
	if strings.ContainsAny(msg, "\n\t") {
		t.Errorf("line breaks stay in the message: %q", msg)
	}
	if !strings.Contains(msg, "<<HERE>>") || !strings.Contains(msg, "Open at that point: reply > action. ") {
		t.Errorf("message: %s", msg)
	}
	if strings.ContainsRune(msg, '�') {
		t.Errorf("a character was cut in half: %q", msg)
	}
}

func TestInvalidJSONRefusalInsideACodeFenceCountsFromTheFencedText(t *testing.T) {
	fenced := "```json\n" + prodExtraBrace + "\n```"
	msg := invalidJSONRefusal(t, fenced)
	if !strings.Contains(msg, "(at byte 510)") || !strings.Contains(msg, "Open at that point: reply > action > payload > ui[0]") {
		t.Errorf("message: %s", msg)
	}
}

func TestTextAfterTheObjectIsLocatedByTheScanner(t *testing.T) {
	const object = `{"state_patch":{},"action":{"type":"goto","state":"done"}}`
	msg := invalidJSONRefusal(t, object+"\nHope this helps!")
	for _, want := range []string{"invalid character after top-level value", fmt.Sprintf("(at byte %d)", len(object)+1), "Around it: ", "<<HERE>>Hope this helps!", "Open at that point: none", resendInstruction} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
}

func TestATruncatedReplyHasNoPositionButStillShowsWhatWasOpen(t *testing.T) {
	msg := invalidJSONRefusal(t, `{"state_patch":{},"action":{"type":"ask_user","payload":{"ui":[{`)
	for _, want := range []string{"(position unknown)", "Open at that point: reply > action > payload > ui[0]", resendInstruction} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "<<HERE>>") {
		t.Errorf("no position, no marker: %s", msg)
	}
}

func TestAReplyThatIsValidJSONButNotAnObjectKeepsItsPlainRefusal(t *testing.T) {
	mustRefuse(t, "teach", "[1]", "reply is not a valid JSON object: expected a JSON object, got an array")
	_, err := parse(t, "teach", "[1]")
	if strings.Contains(err.Error(), "<<HERE>>") || strings.Contains(err.Error(), "Open at that point") {
		t.Errorf("a valid array needs no position: %v", err)
	}
}

func TestAValidReplyStillParsesAlsoInACodeFence(t *testing.T) {
	const object = `{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{"query":"verbs"}}}`
	for _, text := range []string{object, "```json\n" + object + "\n```"} {
		if _, err := parse(t, "teach", text); err != nil {
			t.Errorf("%q: %v", text, err)
		}
	}
}

// The refusal depends on encoding/json's *SyntaxError surviving the JSON
// library's own decoding, which is checked here against the pinned version.
func TestTheJSONLibraryErrorCarriesTheOffset(t *testing.T) {
	_, err := DecodeObject([]byte(`{"a":[1}`))
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) || syntax.Offset != 8 {
		t.Fatalf("want *json.SyntaxError at offset 8, got %T %v", err, err)
	}
}

func TestRedactExcerptRemovesTheQuotedReplyAndKeepsTheRest(t *testing.T) {
	message := invalidJSONRefusal(t, prodExtraBrace)
	// The text the excerpt quotes, on both sides of the place where it broke.
	pos := strings.Index(message, "<<HERE>>")
	start := strings.Index(message, excerptLead) + len(excerptLead)
	end := strings.Index(message, excerptTail)
	if pos < 0 || start < len(excerptLead) || end < pos {
		t.Fatalf("test premise: the refusal does not quote the reply: %s", message)
	}
	before, after := message[start:pos], message[pos+len("<<HERE>>"):end]
	if len(before) < 30 || len(after) < 5 {
		t.Fatalf("test premise: excerpt too short, %q <<HERE>> %q", before, after)
	}

	got := RedactExcerpt(message)

	for _, piece := range []string{before, after, "<<HERE>>", `"Text"`, "GO!", "dataModel"} {
		if strings.Contains(got, piece) {
			t.Errorf("redacted message still holds %q: %s", piece, got)
		}
	}
	for _, kept := range []string{"reply is not a valid JSON object: ", "invalid character '}' after array element", "(at byte 510)", "Around it: " + excerptOmitted + ". Open at that point: reply > action > payload > ui[0].", resendInstruction} {
		if !strings.Contains(got, kept) {
			t.Errorf("redacted message lost %q: %s", kept, got)
		}
	}
}

func TestRedactExcerptWorksInsideTheRetriesExhaustedMessage(t *testing.T) {
	refusal := invalidJSONRefusal(t, prodExtraBrace)
	message := "model gave no valid reply in 3 attempts; last problem: " + refusal
	got := RedactExcerpt(message)
	if !strings.HasPrefix(got, "model gave no valid reply in 3 attempts; last problem: reply is not a valid JSON object: ") || strings.Contains(got, "GO!") || !strings.HasSuffix(got, resendInstruction) {
		t.Errorf("got %s", got)
	}
}

func TestRedactExcerptLeavesAMessageWithoutAnExcerptAlone(t *testing.T) {
	for _, message := range []string{
		"",
		"run used its 20 steps",
		invalidJSONRefusal(t, `{"state_patch":{"a":"b"},"action":`),                    // position unknown
		"reply is not a valid JSON object: json: cannot unmarshal array into Go value", // valid JSON, not an object
	} {
		if got := RedactExcerpt(message); got != message {
			t.Errorf("RedactExcerpt(%q) = %q", message, got)
		}
	}
}

func TestRedactExcerptDoesNotLeakWhenTheExcerptHoldsTheClosingWords(t *testing.T) {
	reply := `{"a":"x. Open at that point: SECRET-TEXT","b":[1,]}`
	message := invalidJSONRefusal(t, reply)
	if !strings.Contains(message, "SECRET-TEXT") {
		t.Fatalf("test premise: the excerpt does not quote the secret: %s", message)
	}
	if got := RedactExcerpt(message); strings.Contains(got, "SECRET-TEXT") {
		t.Errorf("leaked: %s", got)
	}
}
