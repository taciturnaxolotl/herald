package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strings"
	"testing"

	"charm.land/log/v2"
	"charm.land/ssh"
	"github.com/taciturnaxolotl/herald/store"
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

func TestSessionUserComesFromAuthenticatedKey(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := NewServer(Config{AllowAllKeys: true}, st, nil, nil, log.New(io.Discard))
	sshSrv := &ssh.Server{
		PublicKeyHandler: srv.publicKeyHandler,
		Handler: func(sess ssh.Session) {
			user, err := sessionUser(sess, st)
			if err != nil {
				_, _ = io.WriteString(sess, "error: "+err.Error())
				return
			}
			_, _ = io.WriteString(sess, user.PubkeyFP)
		},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = sshSrv.Serve(ln) }()
	defer func() { _ = sshSrv.Close() }()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	client, err := gossh.Dial("tcp", ln.Addr().String(), &gossh.ClientConfig{
		User:            "test",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // test server
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.Output("")
	if err != nil {
		t.Fatal(err)
	}

	want := store.FingerprintSlug(gossh.FingerprintSHA256(signer.PublicKey()))
	if string(out) != want {
		t.Fatalf("session user = %q, want %q", out, want)
	}
}
