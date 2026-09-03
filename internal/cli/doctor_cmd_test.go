package cli

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mcmgr/internal/config"
	"mcmgr/internal/props"
	"mcmgr/internal/server"
)

// newDoctorServer builds a fabric server directory with nothing installed yet,
// which is the state every check has an opinion about.
func newDoctorServer(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Name: "clinic", Minecraft: "1.21.4", Loader: config.Fabric,
		Jar: "fabric-server-launch.jar",
	}
	cfg.SetDir(dir)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestDoctorChecksLaunchTarget(t *testing.T) {
	cfg := newDoctorServer(t)

	line := checkLaunchTarget(cfg)
	if line.state != checkFail {
		t.Errorf("a missing jar should fail, got state %v (%s)", line.state, line.detail)
	}
	if !strings.Contains(line.fix, "mcmgr server install") {
		t.Errorf("the fix should name the install command, got %q", line.fix)
	}
	// The advice belongs on the fix line, not repeated in the detail.
	if strings.Contains(line.detail, "run '") {
		t.Errorf("the detail should not repeat the fix, got %q", line.detail)
	}

	if err := os.WriteFile(filepath.Join(cfg.Dir(), cfg.Jar), []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if line := checkLaunchTarget(cfg); line.state != checkPass {
		t.Errorf("a jar that is present should pass, got %q", line.detail)
	}
}

func TestDoctorChecksEULA(t *testing.T) {
	cfg := newDoctorServer(t)

	line := checkEULA(cfg)
	if line.state != checkFail {
		t.Error("an unaccepted EULA should fail; the server will not start")
	}
	if !strings.Contains(line.fix, "mcmgr server accept-eula") {
		t.Errorf("the fix should name accept-eula, got %q", line.fix)
	}

	if err := server.AcceptEULA(cfg.Dir()); err != nil {
		t.Fatal(err)
	}
	if line := checkEULA(cfg); line.state != checkPass {
		t.Errorf("an accepted EULA should pass, got %q", line.detail)
	}
}

func TestDoctorChecksPort(t *testing.T) {
	cfg := newDoctorServer(t)

	// Take a port, then tell the server to use it: that is exactly the clash
	// the check exists to find before the JVM does.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	p, err := props.Load(cfg.Dir())
	if err != nil {
		t.Fatal(err)
	}
	p.Set("server-port", strconv.Itoa(port))
	p.Set("server-ip", "127.0.0.1")
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}

	line := checkPort(cfg, false)
	if line.state != checkFail {
		t.Errorf("a port already in use should fail, got %q", line.detail)
	}
	if !strings.Contains(line.fix, "mcmgr config set port") {
		t.Errorf("the fix should name the setting, got %q", line.fix)
	}

	// A server that is up holds its own port, so binding proves nothing.
	if line := checkPort(cfg, true); line.state != checkPass {
		t.Error("a running server's port must not be reported as a clash")
	}

	ln.Close()
	if line := checkPort(cfg, false); line.state != checkPass {
		t.Errorf("a free port should pass, got %q", line.detail)
	}
}

// TestDoctorPortDefaultsToMinecrafts covers a server whose properties file does
// not exist yet, which is every server before its first run.
func TestDoctorPortDefaultsToMinecrafts(t *testing.T) {
	cfg := newDoctorServer(t)
	if port, _ := serverAddr(cfg); port != defaultPort {
		t.Errorf("port = %d, want Minecraft's default %d", port, defaultPort)
	}
}

func TestDoctorChecksModsAgainstTheLockfile(t *testing.T) {
	cfg := newDoctorServer(t)
	lock := &config.Lock{Mods: []config.LockedMod{
		{ID: "lithium", Name: "Lithium", Provider: "modrinth", Filename: "lithium.jar", Explicit: true},
	}}
	if err := cfg.SaveLock(lock); err != nil {
		t.Fatal(err)
	}

	line := checkMods(cfg)
	if line.state != checkFail {
		t.Errorf("a locked mod with no jar on disk should fail, got %q", line.detail)
	}
	if !strings.Contains(line.detail, "lithium") {
		t.Errorf("the detail should name the mod, got %q", line.detail)
	}
	if !strings.Contains(line.fix, "mcmgr mod sync") {
		t.Errorf("the fix should be mod sync, got %q", line.fix)
	}

	if err := os.MkdirAll(cfg.ContentPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ContentPath(), "lithium.jar"), []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if line := checkMods(cfg); line.state != checkPass {
		t.Errorf("a lockfile matching what is on disk should pass, got %q", line.detail)
	}

	// A vanilla server has nowhere to put mods, so this is never a failure.
	cfg.Loader = config.Vanilla
	if line := checkMods(cfg); line.state != checkPass {
		t.Errorf("a vanilla server should pass the mods check, got %q", line.detail)
	}
}

// TestDoctorIsRegistered keeps the command reachable and grouped where the
// help promises.
func TestDoctorIsRegistered(t *testing.T) {
	var found *Command
	for _, s := range Root().Sub {
		if s.Name == "doctor" {
			found = s
		}
	}
	if found == nil {
		t.Fatal("mcmgr doctor is not in the command tree")
	}
	if found.Group != "Getting started" {
		t.Errorf("doctor is grouped under %q, want \"Getting started\"", found.Group)
	}
	if found.Run == nil {
		t.Error("doctor should be a leaf that runs")
	}
}
