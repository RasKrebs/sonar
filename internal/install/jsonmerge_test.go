package install

import (
	"testing"
)

func TestParseObjectPreservesKeyOrder(t *testing.T) {
	src := []byte(`{"zebra":1,"apple":2,"mango":{"inner":true},"list":[1,"two",null]}`)
	obj, err := ParseObject(src)
	if err != nil {
		t.Fatalf("ParseObject: %v", err)
	}
	want := []string{"zebra", "apple", "mango", "list"}
	if got := obj.Keys(); !equalStrings(got, want) {
		t.Errorf("Keys() = %v, want %v", got, want)
	}
}

func TestEncodeRoundTripsUnchanged(t *testing.T) {
	src := []byte("{\n  \"b\": 1,\n  \"a\": {\n    \"nested\": [\n      true,\n      2.5\n    ]\n  }\n}\n")
	obj, err := ParseObject(src)
	if err != nil {
		t.Fatalf("ParseObject: %v", err)
	}
	out, err := Encode(obj, "  ")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(out) != string(src) {
		t.Errorf("round trip changed content:\n got: %q\nwant: %q", out, src)
	}
}

func TestSetPreservesPositionAndAppends(t *testing.T) {
	obj, err := ParseObject([]byte(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatalf("ParseObject: %v", err)
	}
	obj.Set("a", 9.0)
	obj.Set("c", 3.0)
	if got := obj.Keys(); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Errorf("Keys() = %v", got)
	}
	out, err := Encode(obj, "")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got := string(out); got != "{\"a\":9,\"b\":2,\"c\":3}\n" {
		t.Errorf("Encode() = %q", got)
	}
}

func TestDeleteRemovesOnlyThatKey(t *testing.T) {
	obj, err := ParseObject([]byte(`{"a":1,"b":2,"c":3}`))
	if err != nil {
		t.Fatalf("ParseObject: %v", err)
	}
	if !obj.Delete("b") {
		t.Fatal("Delete(b) returned false")
	}
	if obj.Delete("b") {
		t.Fatal("second Delete(b) returned true")
	}
	if got := obj.Keys(); !equalStrings(got, []string{"a", "c"}) {
		t.Errorf("Keys() = %v", got)
	}
}

func TestParseObjectRejectsNonObjectAndGarbage(t *testing.T) {
	for _, src := range []string{`[1,2]`, `"hi"`, `{`, `{"a":1,}`, ``} {
		if _, err := ParseObject([]byte(src)); err == nil {
			t.Errorf("ParseObject(%q) = nil error, want error", src)
		}
	}
}

func TestDetectIndent(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"two spaces", "{\n  \"a\": 1\n}\n", "  "},
		{"four spaces", "{\n    \"a\": 1\n}\n", "    "},
		{"tab", "{\n\t\"a\": 1\n}\n", "\t"},
		{"minified falls back", `{"a":1}`, "  "},
		{"empty falls back", "", "  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectIndent([]byte(tc.src)); got != tc.want {
				t.Errorf("DetectIndent = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasComments(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"line comment", "{\n  // hi\n  \"a\": 1\n}", true},
		{"block comment", "{ /* hi */ \"a\": 1 }", true},
		{"slashes inside string", `{"url":"http://x/y"}`, false},
		{"block marker inside string", `{"a":"/* not a comment */"}`, false},
		{"escaped quote then slashes", `{"a":"say \"hi\" // no"}`, false},
		{"plain", `{"a":1}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasComments([]byte(tc.src)); got != tc.want {
				t.Errorf("HasComments(%s) = %v, want %v", tc.src, got, tc.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
