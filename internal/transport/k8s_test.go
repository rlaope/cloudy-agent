package transport

import (
	"errors"
	"testing"
)

func TestCheckVerb(t *testing.T) {
	cases := []struct {
		verb string
		ok   bool
	}{
		{"get", true},
		{"GET", true},
		{"list", true},
		{"watch", true},
		{"create", false},
		{"update", false},
		{"patch", false},
		{"delete", false},
		{"deletecollection", false},
		{"", false},
	}
	for _, c := range cases {
		err := CheckVerb(c.verb)
		if c.ok && err != nil {
			t.Errorf("verb %q: want allowed, got %v", c.verb, err)
		}
		if !c.ok && !errors.Is(err, ErrKubeVerbViolation) {
			t.Errorf("verb %q: want ErrKubeVerbViolation, got %v", c.verb, err)
		}
	}
}
