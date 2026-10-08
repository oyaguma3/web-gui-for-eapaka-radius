package trace

import (
	"regexp"
	"testing"
)

func TestTraceID(t *testing.T) {
	a, b := New(), New()
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(a) || a == b {
		t.Errorf("New() = %q, %q", a, b)
	}
	if got := From(t.Context()); got != "" {
		t.Errorf("From(empty) = %q", got)
	}
	if got := From(With(t.Context(), a)); got != a {
		t.Errorf("From = %q", got)
	}
}
