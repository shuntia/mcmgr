package cli

import (
	"flag"
	"strings"
	"testing"

	"mcmgr/internal/java"
)

// newValues parses the given `mcmgr new` flags, so tests exercise the same
// registration the command does.
func newValues(t *testing.T, args ...string) *newFlagValues {
	t.Helper()
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	var v newFlagValues
	v.register(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return &v
}

// TestChooseJavaUnattendedTouchesNothing pins the scripted behaviour: with no
// flag, a script gets what it always did, and no network call is made to
// decide it.
func TestChooseJavaUnattendedTouchesNothing(t *testing.T) {
	choice, err := chooseJava(newValues(t), false, "1.21.4")
	if err != nil {
		t.Fatal(err)
	}
	if choice.dist != "" || choice.major != 0 {
		t.Errorf("an unattended run without flags should download nothing, got %+v", choice)
	}
}

func TestChooseJavaRefusesContradictoryFlags(t *testing.T) {
	_, err := chooseJava(newValues(t, "--java", "/opt/jdk/bin/java", "--graalvm"), false, "1.21.4")
	if err == nil || !strings.Contains(err.Error(), "--java") {
		t.Errorf("--java with a download flag should be refused, got %v", err)
	}

	_, err = chooseJava(newValues(t, "--java-dist", "temurin", "--graalvm"), false, "1.21.4")
	if err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Errorf("--graalvm with --java-dist temurin should be refused, got %v", err)
	}

	// An explicit path wins outright, and asks nothing.
	choice, err := chooseJava(newValues(t, "--java", "/opt/jdk/bin/java"), true, "1.21.4")
	if err != nil || choice.dist != "" {
		t.Errorf("--java should settle the question, got %+v, %v", choice, err)
	}
}

func TestChooseZGCTrustsADownloadedRelease(t *testing.T) {
	// The Java is about to be downloaded, so the machine is not consulted.
	yes := "true"
	if use, err := chooseZGC(&yes, false, "", 21); err != nil || !use {
		t.Errorf("Java 21 supports ZGC, got %v, %v", use, err)
	}
	if _, err := chooseZGC(&yes, false, "", 8); err == nil {
		t.Error("--zgc=true on a Java 8 download should be an error, not a silent no-op")
	}
	empty := ""
	if use, err := chooseZGC(&empty, false, "", 8); err != nil || use {
		t.Errorf("Java 8 should quietly stay on its default collector, got %v, %v", use, err)
	}
	if use, err := chooseZGC(&empty, false, "", 25); err != nil || !use {
		t.Errorf("an unattended run on Java 25 should default to ZGC, got %v, %v", use, err)
	}
}

func TestDistFlagsResolve(t *testing.T) {
	t.Setenv("MCMGR_HOME", t.TempDir())
	parse := func(args ...string) (java.Distribution, error) {
		fs := flag.NewFlagSet("venv", flag.ContinueOnError)
		var df distFlags
		df.register(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return df.resolve()
	}
	if d, err := parse(); err != nil || d != java.Temurin {
		t.Errorf("no flags should mean Temurin, got %q, %v", d, err)
	}
	if d, err := parse("--graalvm"); err != nil || d != java.GraalVM {
		t.Errorf("--graalvm = %q, %v", d, err)
	}
	if d, err := parse("--dist", "GraalVM"); err != nil || d != java.GraalVM {
		t.Errorf("--dist GraalVM = %q, %v", d, err)
	}
	if d, err := parse("--dist", "graalvm", "--graalvm"); err != nil || d != java.GraalVM {
		t.Errorf("agreeing flags should be fine, got %q, %v", d, err)
	}
	if _, err := parse("--dist", "temurin", "--graalvm"); err == nil {
		t.Error("disagreeing flags should be refused")
	}
	if _, err := parse("--dist", "zulu"); err == nil {
		t.Error("an unknown distribution should be refused")
	}
}
