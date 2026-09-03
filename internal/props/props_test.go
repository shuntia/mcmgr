package props

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPreservesCommentsAndOrder is the property that matters: Minecraft
// rewrites server.properties itself, and a user's comments and key order must
// survive mcmgr editing one value.
func TestPreservesCommentsAndOrder(t *testing.T) {
	dir := t.TempDir()
	original := `#Minecraft server properties
#Thu Jan 01 00:00:00 UTC 2026
motd=Hello
white-list=false

# our own note
max-players=20
custom-plugin-key=keep me
`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.SetBool("white-list", true)
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	for _, want := range []string{
		"#Minecraft server properties",
		"# our own note",
		"white-list=true",
		"custom-plugin-key=keep me",
		"motd=Hello",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "white-list=false") {
		t.Error("old value was not replaced")
	}
	// Order must be unchanged: motd still precedes max-players.
	if strings.Index(got, "motd=") > strings.Index(got, "max-players=") {
		t.Error("key order changed")
	}
}

func TestMissingFileIsEmptyNotAnError(t *testing.T) {
	f, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("a missing server.properties should not be an error: %v", err)
	}
	if f.Exists() {
		t.Error("Exists should be false before the file is written")
	}
	// Defaults must still be readable so settings can be shown pre-first-run.
	if got := f.GetOr("level-name", "world"); got != "world" {
		t.Errorf("GetOr fallback = %q, want world", got)
	}
	f.Set("server-port", "25566")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	if !f.Exists() {
		t.Error("file should exist after Save")
	}
}

func TestBoolAndEscaping(t *testing.T) {
	dir := t.TempDir()
	f, _ := Load(dir)

	if f.Bool("white-list", false) {
		t.Error("absent key should fall back to the given default")
	}
	f.SetBool("white-list", true)
	if !f.Bool("white-list", false) {
		t.Error("white-list should read back as true")
	}

	// A motd with characters that are meaningful in a properties file must
	// round-trip unchanged.
	tricky := `Welcome: 100% fun \ x=y`
	f.Set("motd", tricky)
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Get("motd"); got != tricky {
		t.Errorf("motd round-trip = %q, want %q", got, tricky)
	}
}

func TestSettingValidation(t *testing.T) {
	whitelist, ok := Lookup("white-list")
	if !ok {
		t.Fatal("white-list should be a known setting")
	}
	if err := whitelist.Validate("true"); err != nil {
		t.Errorf("true should be valid for a bool setting: %v", err)
	}
	if err := whitelist.Validate("yes"); err == nil {
		t.Error("yes should be rejected for a bool setting")
	}

	difficulty, _ := Lookup("difficulty")
	if err := difficulty.Validate("hard"); err != nil {
		t.Errorf("hard should be a valid difficulty: %v", err)
	}
	if err := difficulty.Validate("nightmare"); err == nil {
		t.Error("an unlisted difficulty should be rejected")
	}

	port, _ := Lookup("server-port")
	if err := port.Validate("25565"); err != nil {
		t.Errorf("a numeric port should be valid: %v", err)
	}
	if err := port.Validate("nope"); err == nil {
		t.Error("a non-numeric port should be rejected")
	}
}

// TestTrailingWhitespaceSurvivesRoundTrip: Java properties skip whitespace
// after the separator but keep it at the end of a value, so a save triggered
// by editing another key must not strip it.
func TestTrailingWhitespaceSurvivesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("motd=  Hello  \nmax-players=20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Get("motd"); got != "Hello  " {
		t.Errorf("motd = %q, want %q", got, "Hello  ")
	}
	f.Set("max-players", "10")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "motd=Hello  \n") {
		t.Errorf("trailing whitespace lost:\n%q", data)
	}
}

// TestIntRanges covers values the server would refuse or clamp, so they are
// rejected where they are typed instead.
func TestIntRanges(t *testing.T) {
	port, _ := Lookup("server-port")
	for _, bad := range []string{"0", "65536", "-1", "abc", ""} {
		if err := port.Validate(bad); err == nil {
			t.Errorf("server-port %q should be rejected", bad)
		}
	}
	for _, good := range []string{"1", "25565", "65535", " 25565 "} {
		if err := port.Validate(good); err != nil {
			t.Errorf("server-port %q: %v", good, err)
		}
	}

	view, _ := Lookup("view-distance")
	if err := view.Validate("2"); err == nil {
		t.Error("view-distance 2 should be rejected")
	}
	if err := view.Validate("32"); err != nil {
		t.Errorf("view-distance 32: %v", err)
	}

	players, _ := Lookup("max-players")
	if err := players.Validate("0"); err == nil {
		t.Error("max-players 0 should be rejected")
	}
	if err := players.Validate("100000"); err != nil {
		t.Errorf("max-players 100000: %v", err)
	}

	// A plain int with no range still only needs to be a number.
	motd, _ := Lookup("motd")
	if err := motd.Validate("anything at all"); err != nil {
		t.Errorf("motd: %v", err)
	}
}
