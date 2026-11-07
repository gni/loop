package interactive

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestSessionExplorerTerminalEnter(t *testing.T) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	master := os.NewFile(uintptr(fd), "ptmx")
	defer master.Close()
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	if err := db.InitDB(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"terminal-first", "terminal-second"} {
		if err := db.SaveMessage(id, db.Message{Role: "user", Content: id}); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := db.GetSessions()
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the rendered picker before sending keys to a real raw terminal.
	inputDone := make(chan error, 1)
	go func() {
		_ = master.SetReadDeadline(time.Now().Add(5 * time.Second))
		var rendered bytes.Buffer
		buf := make([]byte, 4096)
		for !bytes.Contains(rendered.Bytes(), []byte("enter select")) {
			n, err := master.Read(buf)
			rendered.Write(buf[:n])
			if err != nil {
				inputDone <- err
				slave.Close()
				return
			}
		}
		_, err := master.Write([]byte("\x1b[B\r"))
		inputDone <- err
	}()
	id, fresh, err := RunSessionExplorer(style.GetTheme("plain"), slave, slave)
	if err != nil || fresh || id != sessions[1].SessionID {
		t.Fatalf("terminal selection: id=%q, fresh=%v, err=%v", id, fresh, err)
	}
	if err := <-inputDone; err != nil {
		t.Fatal(err)
	}
}
