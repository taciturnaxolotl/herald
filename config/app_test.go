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

func TestEnvOverrides(t *testing.T) {
	t.Setenv("HERALD_SSH_PORT", "2022")
	t.Setenv("HERALD_SMTP_HOST", "mail.example.com")
	cfg := DefaultAppConfig()
	if err := applyEnvOverrides(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.SSHPort != 2022 || cfg.SMTP.Host != "mail.example.com" {
		t.Errorf("overrides not applied: %+v", cfg)
	}

	t.Setenv("HERALD_HTTP_PORT", "80x")
	if err := applyEnvOverrides(DefaultAppConfig()); err == nil {
		t.Error("a malformed port must be an error, not silently ignored")
	}
}
