package prompt

import (
	"bufio"
	"io"
	"os"
	"strings"
	"testing"
)

// TestLineFeederRationsInput covers the bug where every accessible prompt built
// its own bufio.Scanner: the first prompt read ahead and swallowed the answers
// meant for all the later ones, which then silently kept their defaults.
func TestLineFeederRationsInput(t *testing.T) {
	src := bufio.NewReader(strings.NewReader("first\nsecond\nthird\n"))

	for _, want := range []string{"first", "second", "third"} {
		feeder := &lineFeeder{src: src}
		// A Scanner is what huh uses, and what previously over-read.
		sc := bufio.NewScanner(feeder)
		if !sc.Scan() {
			t.Fatalf("expected to read %q, got nothing", want)
		}
		if got := sc.Text(); got != want {
			t.Errorf("read %q, want %q", got, want)
		}
		if !feeder.got {
			t.Errorf("feeder should report that %q was consumed", want)
		}
		// The feeder must stop after one line so the rest survives.
		if sc.Scan() {
			t.Errorf("feeder yielded a second line %q; it should stop at one", sc.Text())
		}
	}

	// Input is now exhausted, which must be visible rather than silent.
	feeder := &lineFeeder{src: src}
	if _, err := io.ReadAll(feeder); err != nil {
		t.Fatalf("reading an exhausted feeder should just be empty: %v", err)
	}
	if feeder.got {
		t.Error("feeder should report that no line was available at EOF")
	}
}

// TestLineFeederHandlesFinalLineWithoutNewline makes sure a script whose last
// answer has no trailing newline is still delivered.
func TestLineFeederHandlesFinalLineWithoutNewline(t *testing.T) {
	src := bufio.NewReader(strings.NewReader("only"))
	feeder := &lineFeeder{src: src}

	data, err := io.ReadAll(feeder)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "only" {
		t.Errorf("read %q, want %q", data, "only")
	}
	if !feeder.got {
		t.Error("a final line without a newline still counts as an answer")
	}
}

// TestLineFeederPartialReads checks the feeder when the consumer reads in
// pieces smaller than the line.
func TestLineFeederPartialReads(t *testing.T) {
	src := bufio.NewReader(strings.NewReader("abcdef\nnext\n"))
	feeder := &lineFeeder{src: src}

	buf := make([]byte, 2)
	var got []byte
	for {
		n, err := feeder.Read(buf)
		got = append(got, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if string(got) != "abcdef\n" {
		t.Errorf("assembled %q, want %q", got, "abcdef\n")
	}

	// The following line must still be there for the next prompt.
	rest, _ := io.ReadAll(&lineFeeder{src: src})
	if string(rest) != "next\n" {
		t.Errorf("next prompt would read %q, want %q", rest, "next\n")
	}
}

func TestValidators(t *testing.T) {
	if err := ValidateName("survival"); err != nil {
		t.Errorf("a plain name should be valid: %v", err)
	}
	for _, bad := range []string{"", "  ", ".", "..", "my/server", `a:b`} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("%q should be rejected as a name", bad)
		}
	}

	port := ValidateInt("port", 1, 65535)
	if err := port("25565"); err != nil {
		t.Errorf("25565 should be a valid port: %v", err)
	}
	for _, bad := range []string{"0", "70000", "abc", ""} {
		if err := port(bad); err == nil {
			t.Errorf("%q should be rejected as a port", bad)
		}
	}

	nonEmpty := ValidateNonEmpty("version")
	if err := nonEmpty("  "); err == nil {
		t.Error("whitespace should count as empty")
	}
}

// TestAccessibleEnvParsing checks the opt-in switch, including the ways of
// spelling "off".
func TestAccessibleEnvParsing(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"FALSE": false,
		"1":     true,
		"true":  true,
		"yes":   true,
	}
	for value, want := range cases {
		t.Setenv("MCMGR_ACCESSIBLE", value)
		if got := Accessible(); got != want {
			t.Errorf("MCMGR_ACCESSIBLE=%q: Accessible() = %v, want %v", value, got, want)
		}
	}
}

// TestNoInputDisablesPrompting makes sure the escape hatch for scripts wins
// over everything else.
func TestNoInputDisablesPrompting(t *testing.T) {
	t.Setenv("MCMGR_ACCESSIBLE", "1")
	t.Setenv("MCMGR_NO_INPUT", "1")
	if Interactive() {
		t.Error("MCMGR_NO_INPUT must disable prompting even in accessible mode")
	}
}

// TestIsTerminalRejectsDevNull is the whole point of asking the terminal
// driver rather than checking the file mode: /dev/null and NUL are character
// devices, so a mode check called them terminals and "mcmgr run < /dev/null"
// took the interactive path and detached immediately.
func TestIsTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	defer f.Close()

	if isTerminal(f) {
		t.Errorf("%s must not count as a terminal", os.DevNull)
	}

	// A plain file is not one either, which is what a redirect or a pipe in a
	// script looks like.
	tmp, err := os.CreateTemp(t.TempDir(), "in-*")
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	if isTerminal(tmp) {
		t.Error("a regular file must not count as a terminal")
	}
}

// TestInteractiveRespectsNoInput covers the escape hatch scripts rely on.
func TestInteractiveRespectsNoInput(t *testing.T) {
	t.Setenv("MCMGR_ACCESSIBLE", "")
	t.Setenv("MCMGR_NO_INPUT", "1")
	if Interactive() {
		t.Error("MCMGR_NO_INPUT must switch prompting off")
	}
}
