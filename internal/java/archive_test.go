package java

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSafePathRejectsEscapes covers zip-slip: archive entry names are
// attacker-controlled, and a name like "../../evil" must never be written
// outside the directory being unpacked into.
func TestSafePathRejectsEscapes(t *testing.T) {
	dst := t.TempDir()

	for _, bad := range []string{
		"../escape.txt",
		"../../escape.txt",
		"a/../../escape.txt",
		`..\escape.txt`,
	} {
		if _, err := safePath(dst, bad); err == nil {
			t.Errorf("safePath should have rejected %q", bad)
		}
	}

	for _, good := range []string{
		"jdk-25/bin/java.exe",
		"a/b/../c.txt", // stays inside once cleaned
		"file.txt",
	} {
		full, err := safePath(dst, good)
		if err != nil {
			t.Errorf("safePath(%q): %v", good, err)
			continue
		}
		if !strings.HasPrefix(full, dst) {
			t.Errorf("safePath(%q) = %q, which is outside %q", good, full, dst)
		}
	}
}

// TestExtractZipRejectsTraversal builds a hostile archive and checks nothing
// lands outside the target.
func TestExtractZipRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.zip")

	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("../escaped.txt")
	w.Write([]byte("should never be written"))
	zw.Close()
	f.Close()

	dst := filepath.Join(dir, "out")
	if err := extract(archive, dst); err == nil {
		t.Error("extracting a traversing entry should fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); !os.IsNotExist(err) {
		t.Error("a file escaped the extraction directory")
	}
}

func TestExtractZipRoundTrip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "jre.zip")

	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{
		"jdk-25-jre/bin/java.exe": "binary",
		"jdk-25-jre/release":      "JAVA_VERSION=\"25\"",
	} {
		w, cerr := zw.Create(name)
		if cerr != nil {
			t.Fatal(cerr)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()

	dst := filepath.Join(dir, "out")
	if err := extract(archive, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "jdk-25-jre", "release"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "25") {
		t.Errorf("extracted content = %q", got)
	}
}

func TestExtractTarGzRoundTripAndTraversal(t *testing.T) {
	dir := t.TempDir()

	write := func(name string, entries map[string]string) string {
		path := filepath.Join(dir, name)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		for n, body := range entries {
			hdr := &tar.Header{Name: n, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatal(err)
			}
			tw.Write([]byte(body))
		}
		tw.Close()
		gz.Close()
		return path
	}

	good := write("jre.tar.gz", map[string]string{"jdk-25-jre/bin/java": "#!/bin/sh"})
	dst := filepath.Join(dir, "out")
	if err := extract(good, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dst, "jdk-25-jre", "bin", "java"))
	if err != nil {
		t.Fatal(err)
	}
	// The execute bit has to survive, or the unpacked runtime cannot be
	// started. Only checked off Windows, where permission bits are not
	// representable and executability comes from the file extension instead.
	// tar.gz is only ever used on Unix anyway; Windows downloads get a .zip.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Errorf("bin/java lost its execute bit: mode %v", info.Mode())
	}

	evil := write("evil.tar.gz", map[string]string{"../escaped.txt": "no"})
	if err := extract(evil, filepath.Join(dir, "out2")); err == nil {
		t.Error("a traversing tar entry should fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); !os.IsNotExist(err) {
		t.Error("a file escaped the extraction directory")
	}
}

func TestFindJavaHome(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "jdk-25.0.4.1+1-jre")
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "bin", BinaryName()), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := findJavaHome(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != home {
		t.Errorf("findJavaHome = %q, want %q", got, home)
	}

	// A directory with nothing java-shaped in it is an error, not a guess.
	if _, err := findJavaHome(t.TempDir()); err == nil {
		t.Error("findJavaHome should fail when there is no bin/java")
	}
}
