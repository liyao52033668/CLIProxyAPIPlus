package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSetStringIfDifferent(t *testing.T) {
	payload := []byte(`{"a":"b"}`)
	updated := SetStringIfDifferent(payload, "a", "b")
	if string(updated) != `{"a":"b"}` {
		t.Fatalf("expected no change, got %s", string(updated))
	}
	updated = SetStringIfDifferent(payload, "a", "c")
	if got := gjson.GetBytes(updated, "a").String(); got != "c" {
		t.Fatalf("expected a=c, got %q", got)
	}
}

func TestSetBoolIfDifferent(t *testing.T) {
	payload := []byte(`{"a":true}`)
	updated := SetBoolIfDifferent(payload, "a", true)
	if string(updated) != `{"a":true}` {
		t.Fatalf("expected no change, got %s", string(updated))
	}
	updated = SetBoolIfDifferent(payload, "a", false)
	if got := gjson.GetBytes(updated, "a").Bool(); got {
		t.Fatalf("expected a=false")
	}
}
