package output

import (
	"bytes"
	"testing"
)

func TestBufferedModeAccumulatesUntilFlush(t *testing.T) {
	var out bytes.Buffer
	g := New(&out)

	if _, err := g.Write([]byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("buffered guard must not emit before Flush")
	}
	if err := g.Flush(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello " {
		t.Fatalf("flushed output = %q, want %q", out.String(), "hello ")
	}
	if err := g.Flush(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello " {
		t.Fatal("second flush must not re-emit")
	}
}

func TestPassthroughModeWritesThroughAndDivertsDuringTakeover(t *testing.T) {
	var out, errOut bytes.Buffer
	g := NewPassthrough(&out, &errOut)

	if _, err := g.Write([]byte("direct")); err != nil {
		t.Fatal(err)
	}
	if out.String() != "direct" {
		t.Fatalf("passthrough guard must write through: %q", out.String())
	}

	if !g.TakeOver() {
		t.Fatal("first TakeOver must succeed")
	}
	if g.TakeOver() {
		t.Fatal("second TakeOver must report existing ownership")
	}
	if _, err := g.Write([]byte("diverted")); err != nil {
		t.Fatal(err)
	}
	if out.String() != "direct" {
		t.Fatal("taken-over writes must not reach the owned writer")
	}
	if errOut.String() != "diverted" {
		t.Fatalf("taken-over writes must go to stderr: %q", errOut.String())
	}

	g.Restore()
	if g.IsTakenOver() {
		t.Fatal("Restore must release ownership")
	}
	if _, err := g.Write([]byte(" again")); err != nil {
		t.Fatal(err)
	}
	if out.String() != "direct again" {
		t.Fatalf("after Restore writes must go to the owned writer: %q", out.String())
	}
}

func TestBufferedGuardWithoutStderrStaysBufferedDuringTakeover(t *testing.T) {
	var out bytes.Buffer
	g := New(&out)
	if !g.TakeOver() {
		t.Fatal("TakeOver failed")
	}
	if _, err := g.Write([]byte("held")); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("buffered guard with no stderr must keep writes buffered")
	}
	if err := g.Flush(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "held" {
		t.Fatalf("flushed output = %q, want %q", out.String(), "held")
	}
}
