package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcmgr/internal/config"
	"mcmgr/internal/java"
)

func newJavaCfg(t *testing.T, required int) *config.Config {
	t.Helper()
	cfg := &config.Config{Name: "t", Minecraft: "26.2", Loader: config.Fabric}
	cfg.Java.RequiredMajor = required
	cfg.SetDir(t.TempDir())
	return cfg
}

func TestJavaStatusTooOld(t *testing.T) {
	cases := []struct {
		name    string
		status  JavaStatus
		wantOld bool
		wantOK  bool
	}{
		{"older than required", JavaStatus{Required: 25, Info: java.Info{Major: 21}}, true, false},
		{"exactly required", JavaStatus{Required: 25, Info: java.Info{Major: 25}}, false, true},
		{"newer than required", JavaStatus{Required: 21, Info: java.Info{Major: 25}}, false, true},
		// An unknown requirement must not be treated as a failure.
		{"requirement unknown", JavaStatus{Required: 0, Info: java.Info{Major: 21}}, false, true},
		{"no java at all", JavaStatus{Missing: true, Required: 21}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.status.TooOld(); got != c.wantOld {
				t.Errorf("TooOld() = %v, want %v", got, c.wantOld)
			}
			if got := c.status.OK(); got != c.wantOK {
				t.Errorf("OK() = %v, want %v", got, c.wantOK)
			}
		})
	}
}

func TestJavaStatusExplain(t *testing.T) {
	cfg := newJavaCfg(t, 25)

	old := JavaStatus{Required: 25, Info: java.Info{Major: 21, Path: "/usr/bin/java"}}
	msg := old.Explain(cfg)
	for _, want := range []string{"needs Java 25", "Java 21", "mcmgr venv install"} {
		if !strings.Contains(msg, want) {
			t.Errorf("explanation is missing %q:\n%s", want, msg)
		}
	}

	missing := JavaStatus{Missing: true, Required: 25}
	if msg := missing.Explain(cfg); !strings.Contains(msg, "no Java found") {
		t.Errorf("a missing Java should say so, got %q", msg)
	}

	if msg := (JavaStatus{Required: 21, Info: java.Info{Major: 21}}).Explain(cfg); msg != "" {
		t.Errorf("a healthy status should explain nothing, got %q", msg)
	}
}

// TestDiagnoseFailure covers turning a server's own log into an explanation.
func TestDiagnoseFailure(t *testing.T) {
	cases := []struct {
		name string
		log  string
		want string
	}{
		{
			"class version mismatch",
			`Caused by: java.lang.UnsupportedClassVersionError: net/minecraft/bundler/Main has been compiled by a more recent version of the Java Runtime (class file version 69.0), this version of the Java Runtime only recognizes class file versions up to 65.0`,
			"needs Java 25",
		},
		{"eula", "You need to agree to the EULA in order to run the server.", "accept-eula"},
		{"port taken", "[Server thread/WARN]: **** FAILED TO BIND TO PORT!\nFailed to bind to port", "port is already in use"},
		{"out of memory", "java.lang.OutOfMemoryError: Java heap space", "ran out of heap"},
		{"nothing recognisable", "[Server thread/INFO]: Done (6.5s)! For help, type \"help\"", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, config.StateDirName), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ConsoleLogFor(dir), []byte(c.log), 0o644); err != nil {
				t.Fatal(err)
			}
			got := DiagnoseFailure(dir)
			if c.want == "" {
				if got != "" {
					t.Errorf("expected no diagnosis, got %q", got)
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("diagnosis = %q, want it to mention %q", got, c.want)
			}
		})
	}
}

func TestDiagnoseFailureNoLog(t *testing.T) {
	if got := DiagnoseFailure(t.TempDir()); got != "" {
		t.Errorf("with no console log there is nothing to diagnose, got %q", got)
	}
}

// TestDiagnoseFailureScansTail makes sure a long log still yields the answer,
// since the interesting error is at the end.
func TestDiagnoseFailureScansTail(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, config.StateDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	filler := strings.Repeat("[Server thread/INFO]: nothing interesting here\n", 5000)
	log := filler + `UnsupportedClassVersionError: has been compiled by a more recent version of the Java Runtime (class file version 69.0), this version of the Java Runtime only recognizes class file versions up to 65.0`
	if err := os.WriteFile(ConsoleLogFor(dir), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DiagnoseFailure(dir); !strings.Contains(got, "needs Java 25") {
		t.Errorf("the error at the end of a long log was missed, got %q", got)
	}
}
