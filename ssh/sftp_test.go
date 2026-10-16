package ssh

import "testing"

func TestConfigWriterCapsBuffer(t *testing.T) {
	w := &configWriter{}
	if n, err := w.WriteAt([]byte("hello"), 1<<40); err != nil || n != 5 {
		t.Fatalf("far write: n=%d err=%v", n, err)
	}
	if len(w.buffer) != 0 {
		t.Fatalf("far write grew buffer to %d", len(w.buffer))
	}
	if _, err := w.WriteAt(make([]byte, 3*maxUploadBytes), 0); err != nil {
		t.Fatal(err)
	}
	if len(w.buffer) != maxUploadBytes+1 {
		t.Fatalf("buffer = %d, want %d", len(w.buffer), maxUploadBytes+1)
	}
	if _, err := w.WriteAt([]byte("x"), -1); err == nil {
		t.Fatal("negative offset accepted")
	}
}
