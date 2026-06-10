// smoketest is a one-shot SSH client that exercises the auth + bubbletea path
// against a running nightms instance without needing an interactive terminal.
// It captures rendered output for ~1s after the shell opens and asserts that
// the expected sentinel string appears — that's how we verify the right
// screen rendered with the right identity.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func main() {
	var (
		host        = flag.String("host", "127.0.0.1:2223", "host:port")
		user        = flag.String("user", "phase2test", "SSH username (handle)")
		password    = flag.String("password", "phase2-test-passphrase-2026", "password")
		expect      = flag.String("expect", "", "substring that must appear in rendered output (empty = no check)")
		keys        = flag.String("keys", "", "raw bytes to send mid-session, with `\\r` for Enter and `\\e` for Esc")
		preDelay    = flag.Duration("pre-keys", 400*time.Millisecond, "wait before sending each key")
		captureFor  = flag.Duration("capture", 800*time.Millisecond, "how long to read output before quitting")
		printOutput = flag.Bool("print-output", false, "print decoded output for debugging")
		timeout     = flag.Duration("timeout", 10*time.Second, "dial+auth timeout")
	)
	flag.Parse()

	cfg := &ssh.ClientConfig{
		User:            *user,
		Auth:            []ssh.AuthMethod{ssh.Password(*password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         *timeout,
	}

	dialer := &net.Dialer{Timeout: *timeout}
	conn, err := dialer.Dial("tcp", *host)
	if err != nil {
		fail("dial", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(*timeout))

	c, chans, reqs, err := ssh.NewClientConn(conn, *host, cfg)
	if err != nil {
		fail("ssh handshake/auth", err)
	}
	defer c.Close()
	client := ssh.NewClient(c, chans, reqs)

	sess, err := client.NewSession()
	if err != nil {
		fail("session open", err)
	}
	defer sess.Close()

	stdout, err := sess.StdoutPipe()
	if err != nil {
		fail("stdout pipe", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		fail("stdin pipe", err)
	}

	if err := sess.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
		fail("request pty", err)
	}
	if err := sess.Shell(); err != nil {
		fail("start shell", err)
	}

	// Drain stdout for the capture window into a buffer.
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, stdout)
		close(done)
	}()

	// Walk the `-keys` script with a brief delay between each byte so the
	// bubbletea program has a chance to process and re-render between inputs.
	// Escape sequences: \r → CR (Enter), \e → ESC, \\ → literal backslash.
	for _, b := range expandKeys(*keys) {
		time.Sleep(*preDelay)
		_, _ = stdin.Write([]byte{b})
	}

	time.Sleep(*captureFor)
	// Send Ctrl+C to terminate the bubbletea program cleanly so the server
	// logs a graceful disconnect rather than waiting for an idle timeout.
	// The Root model intercepts Ctrl+C globally on every screen.
	_, _ = stdin.Write([]byte{0x03})

	select {
	case <-done:
	case <-time.After(*timeout):
	}
	_ = sess.Close()
	_ = client.Close()

	rendered := stripAnsi(buf.String())
	if *printOutput {
		fmt.Println("---decoded output---")
		fmt.Println(rendered)
		fmt.Println("---end decoded---")
	}

	if *expect != "" {
		if !strings.Contains(rendered, *expect) {
			fmt.Fprintf(os.Stderr, "FAIL: expected substring %q not found in %d bytes of output\n", *expect, len(rendered))
			os.Exit(2)
		}
		fmt.Printf("OK: found %q in rendered output (%d bytes total).\n", *expect, len(rendered))
		return
	}
	fmt.Println("OK: connected, authenticated, shell open (PTY 80x24).")
}

func fail(stage string, err error) {
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		fmt.Fprintf(os.Stderr, "FAIL at %s (exit %d): %v\n", stage, exitErr.ExitStatus(), err)
	} else {
		fmt.Fprintf(os.Stderr, "FAIL at %s: %v\n", stage, err)
	}
	os.Exit(1)
}

// expandKeys turns a user-supplied keys script into raw bytes. `\r` becomes
// CR (Enter), `\e` becomes ESC, `\\` is a literal backslash. Everything else
// passes through verbatim.
func expandKeys(s string) []byte {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			out = append(out, s[i])
			continue
		}
		switch s[i+1] {
		case 'r':
			out = append(out, '\r')
		case 'n':
			out = append(out, '\n')
		case 'e':
			out = append(out, 0x1b)
		case 't':
			out = append(out, '\t')
		case '\\':
			out = append(out, '\\')
		default:
			out = append(out, s[i+1])
		}
		i++
	}
	return out
}

// stripAnsi removes ANSI escape sequences from a stream so substring assertions
// don't trip over SGR codes. Handles CSI (`ESC [ ... letter`), OSC (`ESC ]
// ... ESC \` or `\x07`), and bare `ESC ?` private-mode markers. Good enough for
// smoke checks; not a general-purpose terminal emulator.
func stripAnsi(s string) string {
	var out strings.Builder
	in := []byte(s)
	for i := 0; i < len(in); i++ {
		b := in[i]
		if b != 0x1b {
			if b >= 0x20 || b == '\n' || b == '\t' {
				out.WriteByte(b)
			}
			continue
		}
		if i+1 >= len(in) {
			break
		}
		// ESC followed by '['  → CSI. Skip until a letter byte (final).
		if in[i+1] == '[' {
			i += 2
			for i < len(in) && !(in[i] >= 0x40 && in[i] <= 0x7E) {
				i++
			}
			continue
		}
		// ESC followed by ']' → OSC. Skip until BEL or ESC \.
		if in[i+1] == ']' {
			i += 2
			for i < len(in) && in[i] != 0x07 && !(in[i] == 0x1b && i+1 < len(in) && in[i+1] == '\\') {
				i++
			}
			if i < len(in) && in[i] == 0x1b {
				i++
			}
			continue
		}
		// ESC followed by single intermediate byte → skip both.
		i++
	}
	return out.String()
}
