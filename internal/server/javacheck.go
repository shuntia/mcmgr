package server

import (
	"fmt"
	"os"
	"strings"

	"mcmgr/internal/config"
	"mcmgr/internal/java"
)

// JavaStatus is the result of comparing the Java a server would use against
// the one its Minecraft version needs.
type JavaStatus struct {
	// Info describes the Java that would run, when one was found.
	Info java.Info
	// Required is the feature release Mojang says this Minecraft version needs;
	// 0 when it is not recorded.
	Required int
	// Missing is set when no Java could be found at all.
	Missing bool
	// Err is why detection failed, if it did.
	Err error
}

// TooOld reports whether the Java in use cannot run this server.
func (s JavaStatus) TooOld() bool {
	return !s.Missing && s.Err == nil && s.Required > 0 && s.Info.Major > 0 && s.Info.Major < s.Required
}

// OK reports whether the server can be started as configured.
func (s JavaStatus) OK() bool { return !s.Missing && s.Err == nil && !s.TooOld() }

// CheckJava works out whether the configured Java can run this server. It does
// no network access: the requirement is read from the manifest, where it was
// recorded at install time.
func CheckJava(cfg *config.Config) JavaStatus {
	status := JavaStatus{Required: cfg.Java.RequiredMajor}

	info, err := java.Detect(cfg.JavaBin())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			status.Missing = true
		}
		status.Err = err
		return status
	}
	status.Info = info
	return status
}

// Explain describes a Java problem and how to fix it.
func (s JavaStatus) Explain(cfg *config.Config) string {
	switch {
	case s.Missing:
		return fmt.Sprintf("no Java found (looked for %q)\n"+
			"       install one with:  mcmgr venv install\n"+
			"       or point at an existing JDK:  mcmgr config set java.path <path>", cfg.JavaBin())
	case s.Err != nil:
		return s.Err.Error()
	case s.TooOld():
		return fmt.Sprintf("Minecraft %s needs Java %d, but %s is Java %d\n"+
			"       install the right one with:  mcmgr venv install\n"+
			"       Minecraft will not start on an older Java; there is no compatibility mode",
			cfg.Minecraft, s.Required, s.Info.Path, s.Info.Major)
	}
	return ""
}

// RecordJavaRequirement looks up and stores the Java version this server's
// Minecraft version needs, so later checks need no network. The caller saves
// the manifest.
func RecordJavaRequirement(cfg *config.Config) error {
	if cfg.Minecraft == "" {
		return nil
	}
	major, err := java.Required(cfg.Minecraft)
	if err != nil {
		return err
	}
	cfg.Java.RequiredMajor = major
	return nil
}

// DiagnoseFailure looks at a server's captured console output and turns a known
// failure into a plain explanation.
//
// It exists because the errors that stop a Minecraft server are rarely
// self-explanatory: an UnsupportedClassVersionError names class file versions
// rather than Java releases, and says nothing about which Java to install.
func DiagnoseFailure(dir string) string {
	data, err := os.ReadFile(ConsoleLogFor(dir))
	if err != nil {
		return ""
	}
	out := string(data)
	// Only the tail matters, and it keeps the scan cheap on a long log.
	if len(out) > 64*1024 {
		out = out[len(out)-64*1024:]
	}

	if m, ok := java.ParseClassVersionError(out); ok {
		return fmt.Sprintf("this server needs Java %d, but it was started with Java %d\n"+
			"       install the right one with:  mcmgr venv install %d\n"+
			"       (the \"class file version\" in the error above is Java %d)",
			m.Need, m.Have, m.Need, m.Need)
	}

	switch {
	case strings.Contains(out, "You need to agree to the EULA"):
		return "the Minecraft EULA has not been accepted\n" +
			"       accept it with:  mcmgr server accept-eula"
	case strings.Contains(out, "Failed to bind to port"),
		strings.Contains(out, "Address already in use"):
		return "the server port is already in use\n" +
			"       something else is on that port; change it with:  mcmgr config set port <number>"
	case strings.Contains(out, "java.lang.OutOfMemoryError"):
		return "the server ran out of heap\n" +
			"       give it more with:  mcmgr config set memory <size>"
	}
	return ""
}
