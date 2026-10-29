package verify

import (
	"context"
	"io"
	"testing"

	"charm.land/log/v2"
	"github.com/taciturnaxolotl/herald/store"
)

type fakeMailer struct{ sent []string }

func (m *fakeMailer) SendVerification(to, _, url string) error {
	m.sent = append(m.sent, to+" "+url)
	return nil
}

func TestRequestSendsOnceThenCoolsDown(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	user, _ := st.GetOrCreateUser(ctx, "fp", "key")

	mailer := &fakeMailer{}
	svc := New(st, mailer, log.New(io.Discard), "https://herald.test")

	if status, err := svc.Request(ctx, user.ID, "a@example.com", "fp", "1.2.3.4"); err != nil || status != Sent {
		t.Fatalf("first request = %v, %v", status, err)
	}
	if status, err := svc.Request(ctx, user.ID, "a@example.com", "fp", "1.2.3.4"); err != nil || status != Pending {
		t.Fatalf("second request = %v, %v; want pending", status, err)
	}
	if len(mailer.sent) != 1 {
		t.Fatalf("expected one email, got %v", mailer.sent)
	}
}
