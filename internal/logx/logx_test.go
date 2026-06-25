package logx

import (
	"bytes"
	"os"
	"testing"
)

func TestOutDefaultsToStdout(t *testing.T) {
	if Out != os.Stdout {
		t.Fatalf("Out = %v, want os.Stdout", Out)
	}
}

func TestOutIsSwappable(t *testing.T) {
	orig := Out
	defer func() { Out = orig }()

	var buf bytes.Buffer
	Out = &buf
	if _, err := Out.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := buf.String(); got != "hello" {
		t.Fatalf("buf = %q, want %q", got, "hello")
	}
}
