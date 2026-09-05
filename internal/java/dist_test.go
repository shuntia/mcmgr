package java

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRuntime(t *testing.T) {
	cases := []struct {
		spec string
		want Runtime
	}{
		{"21", Runtime{Temurin, 21}},
		{"temurin-17", Runtime{Temurin, 17}},
		{"graalvm-21", Runtime{GraalVM, 21}},
		{"GraalVM-25", Runtime{GraalVM, 25}},
		{" graal-21 ", Runtime{GraalVM, 21}},
	}
	for _, c := range cases {
		got, err := ParseRuntime(c.spec)
		if err != nil {
			t.Errorf("ParseRuntime(%q): %v", c.spec, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseRuntime(%q) = %+v, want %+v", c.spec, got, c.want)
		}
	}
	for _, bad := range []string{"", "seven", "7", "graalvm-", "zulu-21", "21-graalvm"} {
		if _, err := ParseRuntime(bad); err == nil {
			t.Errorf("ParseRuntime(%q) should fail", bad)
		}
	}
}

// TestRuntimeDirNames pins the store layout: a Temurin runtime keeps the bare
// number it always had, so downloads made before GraalVM was an option are
// still found, and GraalVM lives beside it under its own name.
func TestRuntimeDirNames(t *testing.T) {
	if got := (Runtime{Temurin, 21}).dirName(); got != "21" {
		t.Errorf("Temurin 21 dir = %q, want 21", got)
	}
	if got := (Runtime{GraalVM, 21}).dirName(); got != "graalvm-21" {
		t.Errorf("GraalVM 21 dir = %q, want graalvm-21", got)
	}
	for _, name := range []string{"21", "graalvm-21"} {
		r, ok := runtimeFromDir(name)
		if !ok || r.dirName() != name {
			t.Errorf("runtimeFromDir(%q) = %+v, %v; should round-trip", name, r, ok)
		}
	}
	// Anything else in the store is not a runtime.
	for _, name := range []string{".unpack-21", "graal-21", "temurin-21", "notes", "21.0.2"} {
		if _, ok := runtimeFromDir(name); ok {
			t.Errorf("runtimeFromDir(%q) should not be a runtime", name)
		}
	}
}

func TestInstalledListsBothDistributions(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("MCMGR_CACHE", cache)
	store := filepath.Join(cache, "java")
	for _, dir := range []string{"17", "graalvm-21", "21", ".unpack-graalvm-21", "stray"} {
		bin := filepath.Join(store, dir, "bin", BinaryName())
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		// Not executable, so Detect fails and the directory name is trusted.
		if err := os.WriteFile(bin, []byte("not java"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// An empty directory is not an installed runtime.
	if err := os.MkdirAll(filepath.Join(store, "graalvm-25"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Installed()
	var names []string
	for _, i := range got {
		names = append(names, Runtime{Dist: i.Dist, Major: i.Major}.String())
	}
	want := "21 graalvm-21 17"
	if strings.Join(names, " ") != want {
		t.Errorf("Installed() = %v, want %s (newest first, Temurin before GraalVM)", names, want)
	}

	if bin, ok := Find(Runtime{GraalVM, 21}); !ok || !strings.Contains(bin, "graalvm-21") {
		t.Errorf("Find(graalvm-21) = %q, %v", bin, ok)
	}
	if _, ok := Find(Runtime{GraalVM, 25}); ok {
		t.Error("an empty directory should not count as installed")
	}
	// FindAny prefers the distribution it is told to and falls back to the other.
	if _, rt, ok := FindAny(21, GraalVM); !ok || rt.Dist != GraalVM {
		t.Errorf("FindAny(21, GraalVM) = %+v, %v; want the GraalVM runtime", rt, ok)
	}
	if _, rt, ok := FindAny(17, GraalVM); !ok || rt.Dist != Temurin {
		t.Errorf("FindAny(17, GraalVM) = %+v, %v; want the Temurin fallback", rt, ok)
	}
}

// TestGraalVMNeedsJava17 covers the refusal that saves a pointless download:
// Oracle publishes no GraalVM for the releases older Minecraft needs.
func TestGraalVMNeedsJava17(t *testing.T) {
	t.Setenv("MCMGR_CACHE", t.TempDir())
	_, err := Install(Runtime{GraalVM, 16})
	if err == nil {
		t.Fatal("GraalVM 16 does not exist and should be refused before any network access")
	}
	if !strings.Contains(err.Error(), "Temurin") {
		t.Errorf("the error should point at Temurin, got %q", err)
	}
	if GraalVM.Supports(16) || !GraalVM.Supports(17) || !Temurin.Supports(8) {
		t.Error("Supports: GraalVM starts at 17, Temurin has everything")
	}
}

func TestGraalDownloadNaming(t *testing.T) {
	u, name, err := graalDownload(21)
	if err != nil {
		t.Skipf("no GraalVM for this platform: %v", err)
	}
	if !strings.HasPrefix(name, "graalvm-jdk-21_") {
		t.Errorf("archive name %q should start with graalvm-jdk-21_", name)
	}
	if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".zip") {
		t.Errorf("archive name %q should be an archive", name)
	}
	if want := graalDownloads + "/21/latest/" + name; u != want {
		t.Errorf("url = %q, want %q", u, want)
	}
}

func TestParseDistribution(t *testing.T) {
	for spelling, want := range map[string]Distribution{
		"": Temurin, "temurin": Temurin, "Adoptium": Temurin,
		"graalvm": GraalVM, "GraalVM": GraalVM, "graal": GraalVM, "oracle": GraalVM,
	} {
		got, err := ParseDistribution(spelling)
		if err != nil || got != want {
			t.Errorf("ParseDistribution(%q) = %q, %v; want %q", spelling, got, err, want)
		}
	}
	if _, err := ParseDistribution("zulu"); err == nil {
		t.Error("an unknown distribution should be rejected")
	}
}

// TestDetectRecognisesGraalVM checks the -version banner parsing, since
// GraalVM puts a space in its build name where Temurin has none.
func TestDetectRecognisesGraalVM(t *testing.T) {
	graal := `java version "21.0.4" 2024-07-16 LTS
Java(TM) SE Runtime Environment Oracle GraalVM 21.0.4+8.1 (build 21.0.4+8-LTS-jvmci-23.1-b41)
Java HotSpot(TM) 64-Bit Server VM Oracle GraalVM 21.0.4+8.1 (build 21.0.4+8-LTS-jvmci-23.1-b41, mixed mode, sharing)`
	info, err := parseVersionOutput("/opt/graalvm/bin/java", graal)
	if err != nil {
		t.Fatal(err)
	}
	if info.Major != 21 || info.Dist != GraalVM || info.Runtime != "Oracle GraalVM 21.0.4+8.1" {
		t.Errorf("GraalVM banner parsed as %+v", info)
	}

	ce := `openjdk version "21.0.2" 2024-01-16
OpenJDK Runtime Environment GraalVM CE 21.0.2+13.1 (build 21.0.2+13-jvmci-23.1-b30)
OpenJDK 64-Bit Server VM GraalVM CE 21.0.2+13.1 (build 21.0.2+13-jvmci-23.1-b30, mixed mode, sharing)`
	info, err = parseVersionOutput("java", ce)
	if err != nil {
		t.Fatal(err)
	}
	if info.Dist != GraalVM || info.Runtime != "GraalVM CE 21.0.2+13.1" {
		t.Errorf("GraalVM CE banner parsed as %+v", info)
	}

	temurin := `openjdk version "21.0.4" 2024-07-16 LTS
OpenJDK Runtime Environment Temurin-21.0.4+7 (build 21.0.4+7-LTS)
OpenJDK 64-Bit Server VM Temurin-21.0.4+7 (build 21.0.4+7-LTS, mixed mode, sharing)`
	info, err = parseVersionOutput("java", temurin)
	if err != nil {
		t.Fatal(err)
	}
	if info.Dist != Temurin || info.Runtime != "Temurin-21.0.4+7" || info.Major != 21 {
		t.Errorf("Temurin banner parsed as %+v", info)
	}

	other := `openjdk version "17.0.2" 2022-01-18
OpenJDK Runtime Environment (build 17.0.2+8-86)
OpenJDK 64-Bit Server VM (build 17.0.2+8-86, mixed mode, sharing)`
	info, err = parseVersionOutput("java", other)
	if err != nil {
		t.Fatal(err)
	}
	if info.Dist != "" || info.Major != 17 {
		t.Errorf("a plain OpenJDK banner should have no distribution, got %+v", info)
	}
}
