package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCredential(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credential")
	want := strings.Repeat("A", 43)
	if e := os.WriteFile(p, []byte(want+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	got, e := ReadCredential(p)
	if e != nil || got != want {
		t.Fatalf("%q %v", got, e)
	}
	os.WriteFile(p, []byte("bad token"), 0600)
	if _, e := ReadCredential(p); e == nil {
		t.Fatal("invalid accepted")
	}
}

func TestReadAgentToken(t *testing.T) {
	token := strings.Repeat("A", 43)
	if got, err := ParseAgentToken(" " + token + "\n"); got != token || err != nil {
		t.Fatal("valid token rejected")
	}
	for _, valid := range []string{"moori_agent_" + token, "moori_agent_" + strings.Repeat("A", 31)} {
		if got, err := ParseAgentToken(valid); err != nil || got != valid {
			t.Fatal("valid token representation rejected")
		}
	}
	for _, malformed := range []string{"", "https://example.com", token + "A", token[:len(token)-1] + "B"} {
		if got, err := ParseAgentToken(malformed); got != "" || err == nil || malformed != "" && strings.Contains(err.Error(), malformed) {
			t.Fatal("malformed token accepted or echoed")
		}
	}
}
