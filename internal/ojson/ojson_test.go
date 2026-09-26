package ojson

import "testing"

// 固定的期望输出：紧凑模式和两格缩进各一份，覆盖转义、大整数、-0.0 和各种浮点写法。
const compact = "{\"a\": \"<>&\\\"\\\\\\n\\t\\b\\f\\u0001\x7f 中\", \"n\": [1, 2.5, 1e+16, 1e-05, 0.1, 123456789012345678901234567890, -0.0, 1000000000000000.0, 3.14e-07], \"e\": {}, \"l\": [], \"nested\": {\"x\": [{\"y\": null, \"z\": true}]}}"

const indented = "{\n  \"a\": \"<>&\\\"\\\\\\n\\t\\b\\f\\u0001\x7f 中\",\n  \"n\": [\n    1,\n    2.5,\n    1e+16,\n    1e-05,\n    0.1,\n    123456789012345678901234567890,\n    -0.0,\n    1000000000000000.0,\n    3.14e-07\n  ],\n  \"e\": {},\n  \"l\": [],\n  \"nested\": {\n    \"x\": [\n      {\n        \"y\": null,\n        \"z\": true\n      }\n    ]\n  }\n}"

func TestRoundTripKeepsFormat(t *testing.T) {
	value, err := Decode([]byte(compact))
	if err != nil {
		t.Fatal(err)
	}
	if got := Dumps(value, -1); got != compact {
		t.Errorf("compact:\n got %q\nwant %q", got, compact)
	}
	if got := Dumps(value, 2); got != indented {
		t.Errorf("indent:\n got %q\nwant %q", got, indented)
	}
}

func TestDuplicateKeysKeepFirstPositionLastValue(t *testing.T) {
	value, err := Decode([]byte(`{"a": 1, "b": 2, "a": 3}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := Dumps(value, -1); got != `{"a": 3, "b": 2}` {
		t.Fatal(got)
	}
}

func TestRejectsTrailingDataAndInvalidUTF8(t *testing.T) {
	if _, err := Decode([]byte(`{} {}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
	if _, err := Decode([]byte("\"\xff\"")); err == nil {
		t.Fatal("invalid utf-8 accepted")
	}
}
