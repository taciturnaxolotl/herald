package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"strings"
	"testing"

	"charm.land/log/v2"
	gossh "golang.org/x/crypto/ssh"
)

func TestAllowedFingerprintsAcceptsDocumentedFormat(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(key))) + " user@host"

	fps := allowedFingerprints([]string{line, "not a key"}, log.New(io.Discard))
	if !fps[gossh.FingerprintSHA256(key)] {
		t.Fatalf("key with comment %q was not allowed", line)
	}
	if len(fps) != 1 {
		t.Fatalf("expected 1 allowed key, got %d", len(fps))
	}
}
