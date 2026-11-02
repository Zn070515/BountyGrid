package model

import "testing"

func TestModelResponseUsable(t *testing.T) {
	cases := []struct {
		name string
		resp ModelResponse
		want bool
	}{
		{"content", ModelResponse{Content: "ok"}, true},
		{"blank", ModelResponse{Content: "   "}, false},
		{"truncated", ModelResponse{Content: "ok", Truncated: true}, false},
		{"empty", ModelResponse{}, false},
	}
	for _, c := range cases {
		if got := c.resp.Usable(); got != c.want {
			t.Errorf("%s: Usable() = %v, want %v", c.name, got, c.want)
		}
	}
}
