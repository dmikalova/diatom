package gate

import (
	"context"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	res, err := Run(ctx, dir, "echo ok")
	if err != nil || !res.Passed || res.Output != "ok" {
		t.Errorf("passing gate = %+v, %v", res, err)
	}
	res, err = Run(ctx, dir, "echo broken >&2; exit 3")
	if err != nil || res.Passed || res.Output != "broken" {
		t.Errorf("failing gate = %+v, %v", res, err)
	}
	if _, err := Run(ctx, dir, "  "); err == nil {
		t.Error("an empty gate ran")
	}
	if _, err := Run(ctx, dir+"/missing", "true"); err == nil {
		t.Error("a gate in a missing directory reported no error")
	}
}

func TestTail(t *testing.T) {
	if got := Tail("a\nb\n", 5); got != "a\nb" {
		t.Errorf("Tail short = %q", got)
	}
	got := Tail("1\n2\n3\n4\n", 2)
	if !strings.HasPrefix(got, "[2 earlier lines cut]") || !strings.HasSuffix(got, "3\n4") {
		t.Errorf("Tail long = %q", got)
	}
}
