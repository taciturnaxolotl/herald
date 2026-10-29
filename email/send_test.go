package email

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestValidateConfigGivesUpOnSilentServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
		}
	}()

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	m, err := NewMailer(SMTPConfig{Host: "127.0.0.1", Port: p, From: "herald@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	m.timeout = 200 * time.Millisecond

	done := make(chan error, 1)
	go func() { done <- m.ValidateConfig() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a server that never greets must fail validation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ValidateConfig hung on a silent server")
	}
}

func TestNewMailerRejectsUnusableDKIMKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))

	if _, err := NewMailer(SMTPConfig{DKIMPrivateKey: pemKey}); err == nil {
		t.Error("a key without domain and selector must be rejected")
	}
	if _, err := NewMailer(SMTPConfig{DKIMPrivateKey: "MIIEpAIBAAKCAQEA", DKIMDomain: "d", DKIMSelector: "s"}); err == nil {
		t.Error("a key without PEM armor must be rejected")
	}
	envStyle := strings.ReplaceAll(pemKey, "\n", `\n`)
	m, err := NewMailer(SMTPConfig{DKIMPrivateKey: envStyle, DKIMDomain: "d", DKIMSelector: "s"})
	if err != nil || m.dkimKey == nil {
		t.Fatalf("a .env style key should load, got %v", err)
	}
}
