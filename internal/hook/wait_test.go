package hook

import (
	"bytes"
	"strings"
	"testing"
)

func TestBlockedWait(t *testing.T) {
	allowed := []string{
		"mage ci:check 2>&1 | tail -30",
		"go test ./... && echo ok",
		"go vet ./... &> vet.log",
		"make 2>&1 >&2 |& tee log",
		`echo "a & b"`,
		`grep -n 'sleep' main.go`,
		`diatom task note 0001 "nohup is blocked"`,
		"sleepy --help",
	}
	for _, c := range allowed {
		if b := BlockedWait(c); b != "" {
			t.Errorf("BlockedWait(%q) = %q, want allowed", c, b)
		}
	}
	blocked := map[string]string{
		"sleep 120; tail -5 tmp/gate.log":                            "sleep 120",
		"(mage ci:check > tmp/check.log 2>&1) & sleep 28":            "&",
		"mkdir -p tmp && nohup sh -c 'mage ci:check' > tmp/gate.log": "nohup sh -c mage ci:check > tmp/gate.log",
		"setsid mage ci:check":                                       "setsid mage ci:check",
		"sh -c 'sleep 5'":                                            "sleep 5",
		"cd x; env A=1 sleep 1":                                      "env A=1 sleep 1",
	}
	for c, want := range blocked {
		if b := BlockedWait(c); b != want {
			t.Errorf("BlockedWait(%q) = %q, want %q", c, b, want)
		}
	}
}

func TestPreToolUseBlocksWaiting(t *testing.T) {
	var out bytes.Buffer
	if err := PreToolUse(
		strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"sleep 120; tail x"}}`),
		&out,
		Scope{},
	); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"deny"`)) ||
		!bytes.Contains(out.Bytes(), []byte("narrower check")) {
		t.Errorf("output %s", out.String())
	}
}
