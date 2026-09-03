package server

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestVersionInLine(t *testing.T) {
	cases := map[string]string{
		// Vanilla and Fabric both print the standard banner.
		`[12:00:01] [Server thread/INFO]: Starting minecraft server version 1.21.4`: "1.21.4",
		// Paper's own banner.
		`[12:00:02] [Server thread/INFO]: This server is running Paper version 1.21.4-232 (MC: 1.21.4)`: "1.21.4",
		// Paperclip names the jar it downloads, long before any banner.
		`Downloading mojang_1.21.4.jar`: "1.21.4",
		// The vanilla bundler extracts the real server jar under versions/.
		`Unpacking versions/1.20.6/server-1.20.6.jar`: "1.20.6",
		// Snapshots and release candidates are versions too.
		`Starting minecraft server version 25w14a`:     "25w14a",
		`Starting minecraft server version 1.21.4-rc1`: "1.21.4-rc1",

		// Lines that must not be mistaken for a version.
		`[12:00:00] [main/INFO]: Loading libraries, please wait...`: "",
		`You need to agree to the EULA in order to run the server.`: "",
		``: "",
	}
	for line, want := range cases {
		if got := versionInLine(line); got != want {
			t.Errorf("versionInLine(%q) = %q, want %q", line, got, want)
		}
	}
}

// TestVersionScannerAcrossChunks checks that a version split across two writes
// is still seen, since process output arrives in arbitrary chunks.
func TestVersionScannerAcrossChunks(t *testing.T) {
	s := &versionScanner{found: make(chan string, 1)}

	s.Write([]byte("[12:00:00] [main/INFO]: Loading li"))
	s.Write([]byte("braries\n[12:00:01] [Server thread/INFO]: Starting min"))
	if got := s.Result().Version; got != "" {
		t.Errorf("no complete line yet, but got %q", got)
	}
	s.Write([]byte("ecraft server version 1.21.4\n"))

	if got := s.Result().Version; got != "1.21.4" {
		t.Errorf("Result = %q, want 1.21.4", got)
	}
	select {
	case v := <-s.found:
		if v != "1.21.4" {
			t.Errorf("signalled %q, want 1.21.4", v)
		}
	case <-time.After(time.Second):
		t.Error("the version was found but never signalled")
	}
}

// TestVersionScannerSignalsOnce makes sure a second matching line cannot block
// on the single-slot channel.
func TestVersionScannerSignalsOnce(t *testing.T) {
	s := &versionScanner{found: make(chan string, 1)}
	for i := 0; i < 5; i++ {
		s.Write([]byte("Starting minecraft server version 1.21.4\n"))
	}
	if got := s.Result().Version; got != "1.21.4" {
		t.Errorf("Result = %q, want 1.21.4", got)
	}
	if len(s.found) != 1 {
		t.Errorf("found channel holds %d values, want exactly 1", len(s.found))
	}
}

func TestVersionScannerKeepsTail(t *testing.T) {
	s := &versionScanner{found: make(chan string, 1)}
	s.Write([]byte("first line\nsecond line\nyou need to agree to the EULA\n"))
	if !strings.Contains(s.Tail(), "EULA") {
		t.Errorf("Tail = %q, want it to keep the last output for diagnostics", s.Tail())
	}
}

func TestProbeRejectsMissingJava(t *testing.T) {
	if _, err := ProbeJar("whatever.jar", "definitely-not-a-real-java-binary", time.Second); err == nil {
		t.Error("probing with a missing java should fail cleanly")
	}
}

func TestProbeRejectsMissingJar(t *testing.T) {
	if _, err := ProbeJar("no-such-file.jar", "", time.Second); err == nil {
		t.Error("probing a jar that does not exist should fail")
	}
}

// TestProbeRealJar runs the probe against an actual server jar. It is opt-in
// because it needs a JDK, a real jar and, for Paper and Fabric, network access
// to fetch the game itself.
//
//	MCMGR_TEST_JAR=/path/to/server.jar go test -run TestProbeRealJar -v ./internal/server/
func TestProbeRealJar(t *testing.T) {
	jar := os.Getenv("MCMGR_TEST_JAR")
	if jar == "" {
		t.Skip("set MCMGR_TEST_JAR to a real server jar to run this")
	}
	want := os.Getenv("MCMGR_TEST_JAR_VERSION")

	// The directory holding the jar must be left exactly as it was found.
	before, err := os.ReadDir(dirOf(jar))
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	got, err := ProbeJar(jar, "", 4*time.Minute)
	if err != nil {
		t.Fatalf("ProbeJar: %v", err)
	}
	t.Logf("probed %s in %s: version=%s loader=%s %s", jar, time.Since(start).Round(time.Millisecond), got.Version, got.Loader, got.LoaderVer)

	if want != "" && got.Version != want {
		t.Errorf("probed version = %q, want %q", got.Version, want)
	}

	after, err := os.ReadDir(dirOf(jar))
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Errorf("the probe changed the jar's directory: %d entries before, %d after",
			len(before), len(after))
	}
}

func dirOf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[:i]
	}
	return "."
}
