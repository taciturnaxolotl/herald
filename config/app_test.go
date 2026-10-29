package config

import "testing"

func TestExpandEnvLeavesBareDollarsAlone(t *testing.T) {
	t.Setenv("SMTP_PASS", "s3cret")
	t.Setenv("abc", "SHOULD-NOT-APPEAR")

	got := expandEnv(`pass: ${SMTP_PASS}
other: "pa$$word x$abc $"`)
	want := `pass: s3cret
other: "pa$$word x$abc $"`
	if got != want {
		t.Errorf("expandEnv:\n got %q\nwant %q", got, want)
	}
}
