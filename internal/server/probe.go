package server

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mcmgr/internal/config"
)

// DefaultProbeTimeout bounds how long a probe may run. Vanilla reports its
// version in seconds; Paper and Fabric first fetch and patch the game jar,
// which is why this is generous.
const DefaultProbeTimeout = 3 * time.Minute

// probeTailBytes is how much output is kept to explain a failed probe.
const probeTailBytes = 4096

// ProbeJar asks the server jar what version it is by starting it and
// reading its own startup banner, then stopping it.
//
// This is the last resort, used only when nothing inside the jar or the
// directory identified the version. It is safe to run against a live server
// directory because it never touches it: the jar is started in a throwaway
// temporary directory, so the world, configuration and logs are untouched, and
// the copy being probed writes its own files there instead.
//
// Two things make it terminate on its own even if the version never appears:
// no eula.txt is created in the temporary directory, so the server prints its
// version, refuses to start and exits within seconds; and the process is
// killed the moment the version is seen, or when the timeout expires.
func ProbeJar(jarPath, javaBin string, timeout time.Duration) (ProbeResult, error) {
	if javaBin == "" {
		javaBin = "java"
	}
	if _, err := exec.LookPath(javaBin); err != nil {
		return ProbeResult{}, fmt.Errorf("java not found (looked for %q), so the jar cannot be probed", javaBin)
	}
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}

	abs, err := filepath.Abs(jarPath)
	if err != nil {
		return ProbeResult{}, err
	}
	if _, err := os.Stat(abs); err != nil {
		return ProbeResult{}, err
	}

	// Everything the probed server writes lands here and is deleted after.
	sandbox, err := os.MkdirTemp("", "mcmgr-probe-")
	if err != nil {
		return ProbeResult{}, err
	}
	defer os.RemoveAll(sandbox)

	// Port 0 lets the OS pick a free one, so a probe can never collide with
	// the real server even if it got as far as binding, which it should not.
	_ = os.WriteFile(filepath.Join(sandbox, "server.properties"),
		[]byte("server-port=0\n"), 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	scanner := &versionScanner{found: make(chan string, 1)}

	cmd := exec.CommandContext(ctx, javaBin, "-jar", abs, "nogui")
	cmd.Dir = sandbox
	hideConsole(cmd)
	// Writing to a scanner rather than reading from a pipe means Wait cannot
	// block on an undrained pipe once the process is killed early.
	cmd.Stdout = scanner
	cmd.Stderr = scanner

	if err := cmd.Start(); err != nil {
		return ProbeResult{}, fmt.Errorf("starting %s to probe the jar: %w", javaBin, err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case <-scanner.found:
		cancel()   // stop the server as soon as it has told us what we needed
		<-waitDone // and reap it, so no JVM is left behind
		return scanner.Result(), nil

	case <-waitDone:
		// It exited on its own, which is the normal path when the EULA gate
		// stops it. The version may still have appeared before it did.
		if r := scanner.Result(); r.Version != "" {
			return r, nil
		}
		return ProbeResult{}, fmt.Errorf("the server exited without reporting a version\n%s",
			indentBlock(scanner.Tail(), "       "))

	case <-ctx.Done():
		<-waitDone
		if r := scanner.Result(); r.Version != "" {
			return r, nil
		}
		return ProbeResult{}, fmt.Errorf("gave up probing after %s\n%s",
			timeout, indentBlock(scanner.Tail(), "       "))
	}
}

// ProbeResult is what starting the jar revealed. Loader is filled in only when
// the server named its own platform before the probe stopped it.
type ProbeResult struct {
	Version   string
	Loader    config.Loader
	LoaderVer string
}

// paperclipPattern matches the vanilla jar Paperclip downloads before
// patching, e.g. "mojang_1.21.4.jar", which appears well before any banner.
var paperclipPattern = regexp.MustCompile(`mojang_([0-9][\w.\-]*)\.jar`)

// bundlerPattern matches the bundled server the vanilla launcher extracts,
// e.g. "versions/1.21.4/server-1.21.4.jar".
var bundlerPattern = regexp.MustCompile(`versions[/\\]([0-9][\w.\-]*)[/\\]server-`)

// modLoaderPattern matches the line Fabric and Quilt print as they boot, which
// names the game version and the loader version together:
//
//	Loading Minecraft 1.21.4 with Fabric Loader 0.16.14
var modLoaderPattern = regexp.MustCompile(`(?i)loading minecraft ([0-9][\w.\-]*) with (fabric|quilt) loader ([\w.\-]+)`)

// paperBannerPattern matches Paper's own "running Paper version" banner.
var paperBannerPattern = regexp.MustCompile(`(?i)running (paper|purpur|folia) version`)

// versionInLine returns the Minecraft version mentioned in one line of server
// output, or "".
func versionInLine(line string) string {
	if m := modLoaderPattern.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	for _, re := range []*regexp.Regexp{
		startupVersionPattern, runningVersionPattern, paperclipPattern, bundlerPattern,
	} {
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// loaderInLine returns the platform a line identifies, if any. A probe picks
// this up for free while it waits for the version.
func loaderInLine(line string) (config.Loader, string) {
	if m := modLoaderPattern.FindStringSubmatch(line); m != nil {
		if strings.EqualFold(m[2], "quilt") {
			return config.Quilt, m[3]
		}
		return config.Fabric, m[3]
	}
	if paperBannerPattern.MatchString(line) {
		return config.Paper, ""
	}
	if strings.Contains(strings.ToLower(line), "neoforge") {
		return config.NeoForge, ""
	}
	return "", ""
}

// versionScanner is an io.Writer that watches server output for a version and
// keeps the tail for diagnostics.
type versionScanner struct {
	mu        sync.Mutex
	partial   []byte
	tail      []byte
	result    string
	loader    config.Loader
	loaderVer string
	found     chan string
	once      sync.Once
}

func (v *versionScanner) Write(p []byte) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.tail = append(v.tail, p...)
	if len(v.tail) > probeTailBytes {
		v.tail = v.tail[len(v.tail)-probeTailBytes:]
	}

	v.partial = append(v.partial, p...)
	for {
		i := bytes.IndexByte(v.partial, '\n')
		if i < 0 {
			break
		}
		line := string(bytes.TrimRight(v.partial[:i], "\r"))
		v.partial = v.partial[i+1:]

		// The loader is recorded whenever it appears, but is never waited for:
		// the version is what the probe exists to find.
		if loader, loaderVer := loaderInLine(line); loader != "" && v.loader == "" {
			v.loader, v.loaderVer = loader, loaderVer
		}
		if version := versionInLine(line); version != "" && v.result == "" {
			v.result = version
			v.once.Do(func() { v.found <- version })
		}
	}
	// A very long line without a newline should not grow without bound.
	if len(v.partial) > 64*1024 {
		v.partial = v.partial[len(v.partial)-1024:]
	}
	return len(p), nil
}

// Result returns everything seen so far.
func (v *versionScanner) Result() ProbeResult {
	v.mu.Lock()
	defer v.mu.Unlock()
	return ProbeResult{Version: v.result, Loader: v.loader, LoaderVer: v.loaderVer}
}

// Tail returns the last of the output, for error messages.
func (v *versionScanner) Tail() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return strings.TrimSpace(string(v.tail))
}

// indentBlock indents every line of s, keeping error output readable.
func indentBlock(s, prefix string) string {
	if s == "" {
		return prefix + "(no output)"
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	for i := range lines {
		lines[i] = prefix + strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}
