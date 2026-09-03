package java

import (
	"strings"
	"testing"
)

// TestParseClassVersionError covers the error that started this: an
// UnsupportedClassVersionError names class file versions, not Java releases,
// so it has to be translated before it means anything.
func TestParseClassVersionError(t *testing.T) {
	reported := `[21:07:41] [ERROR] [FabricLoader/]: Uncaught exception in thread "main"
java.lang.RuntimeException: An exception occurred when launching the server!
Caused by: java.lang.UnsupportedClassVersionError: net/minecraft/bundler/Main has been compiled by a more recent version of the Java Runtime (class file version 69.0), this version of the Java Runtime only recognizes class file versions up to 65.0
        at java.base/java.lang.ClassLoader.defineClass1(Native Method)`

	m, ok := ParseClassVersionError(reported)
	if !ok {
		t.Fatal("the reported error should be recognised")
	}
	if m.Need != 25 {
		t.Errorf("Need = %d, want 25 (class file 69)", m.Need)
	}
	if m.Have != 21 {
		t.Errorf("Have = %d, want 21 (class file 65)", m.Have)
	}
	if !strings.Contains(m.Error(), "Java 25") {
		t.Errorf("the message should name the needed release, got %q", m.Error())
	}
}

func TestParseClassVersionErrorOtherVersions(t *testing.T) {
	cases := []struct{ need, have, wantNeed, wantHave int }{
		{61, 52, 17, 8},  // Java 17 classes on Java 8
		{65, 61, 21, 17}, // Java 21 classes on Java 17
		{66, 65, 22, 21},
	}
	for _, c := range cases {
		text := "has been compiled by a more recent version of the Java Runtime (class file version " +
			itoa(c.need) + ".0), this version of the Java Runtime only recognizes class file versions up to " +
			itoa(c.have) + ".0"
		m, ok := ParseClassVersionError(text)
		if !ok {
			t.Errorf("class file %d/%d was not recognised", c.need, c.have)
			continue
		}
		if m.Need != c.wantNeed || m.Have != c.wantHave {
			t.Errorf("class file %d/%d gave Java %d/%d, want %d/%d",
				c.need, c.have, m.Need, m.Have, c.wantNeed, c.wantHave)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestParseClassVersionErrorIgnoresOtherOutput makes sure ordinary server logs
// are not mistaken for a version problem.
func TestParseClassVersionErrorIgnoresOtherOutput(t *testing.T) {
	for _, text := range []string{
		"",
		"[Server thread/INFO]: Done (6.571s)! For help, type \"help\"",
		"java.lang.OutOfMemoryError: Java heap space",
		"You need to agree to the EULA in order to run the server.",
		// A truncated version of the message must not half-match.
		"has been compiled by a more recent version of the Java Runtime",
	} {
		if _, ok := ParseClassVersionError(text); ok {
			t.Errorf("%q should not be read as a class version error", text)
		}
	}
}

// TestMajorFromVersion covers both Java version schemes: 1.8.0_402 for the old
// one and 21.0.12 for the modern one.
func TestMajorFromVersion(t *testing.T) {
	cases := map[string]int{
		"21.0.12":   21,
		"25.0.4.1":  25,
		"17":        17,
		"1.8.0_402": 8,
		"1.7.0_80":  7,
		"11.0.2":    11,
		"25-ea+1":   25,
		"nonsense":  0,
		"":          0,
	}
	for in, want := range cases {
		if got := majorFromVersion(in); got != want {
			t.Errorf("majorFromVersion(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestDetectRealJava runs against whatever Java is on this machine, which is
// the only way to know the parser matches real output.
func TestDetectRealJava(t *testing.T) {
	info, err := Detect("java")
	if err != nil {
		t.Skipf("no java on PATH: %v", err)
	}
	if info.Major < 8 {
		t.Errorf("Major = %d, which cannot be right", info.Major)
	}
	if info.Version == "" {
		t.Error("Version should not be empty")
	}
	if info.Path == "" {
		t.Error("Path should be the resolved binary")
	}
	t.Logf("detected Java %d: %s at %s", info.Major, info.String(), info.Path)
}

func TestDetectMissingJava(t *testing.T) {
	if _, err := Detect("definitely-not-a-java-binary-here"); err == nil {
		t.Error("detecting a nonexistent binary should fail")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("the error should say it was not found, got %q", err)
	}
}
