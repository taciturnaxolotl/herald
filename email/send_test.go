package email

import (
	"net"
	"strconv"
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
