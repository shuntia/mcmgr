package server

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"mcmgr/internal/config"
	"mcmgr/internal/ui"
)

// launchTimeout bounds how long `server launch` waits for the detached
// supervisor to come up and answer its control port.
const launchTimeout = 30 * time.Second

// Launch starts the server in a detached supervisor process and waits until it
// is answering on its control port. The supervisor outlives the shell that
// started it.
func Launch(cfg *config.Config, opts Options) (*State, error) {
	if live, err := Probe(cfg.Dir()); err != nil {
		return nil, err
	} else if live != nil {
		return nil, fmt.Errorf("server %q is already running (pid %d)", cfg.Name, live.State.PID)
	}
	if err := preflight(cfg); err != nil {
		return nil, err
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locating the mcmgr executable: %w", err)
	}
	if err := os.MkdirAll(cfg.StatePath(), 0o755); err != nil {
		return nil, err
	}

	// The detached supervisor has no console, so its own diagnostics go to a
	// file that is shown if startup fails.
	logPath := SupervisorLogFor(cfg.Dir())
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	args := []string{"server", "_supervise", "--dir", cfg.Dir()}
	for _, a := range opts.ExtraJavaArgs {
		args = append(args, "--java-arg", a)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = cfg.Dir()
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detach(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawning supervisor: %w", err)
	}
	supPID := cmd.Process.Pid
	// Do not reap it: the supervisor is meant to outlive this process.
	_ = cmd.Process.Release()

	deadline := time.Now().Add(launchTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(120 * time.Millisecond)

		if live, _ := Probe(cfg.Dir()); live != nil && live.Responsive {
			return live.State, nil
		}
		if !processAlive(supPID) {
			return nil, fmt.Errorf("supervisor exited during startup:\n%s", indentTail(logPath, 12))
		}
	}
	return nil, fmt.Errorf("timed out after %s waiting for the server to start; see %s",
		launchTimeout, logPath)
}

// Attach streams a running server's console and forwards typed lines to it.
// Ctrl-C detaches without stopping the server, which is the whole point of
// running detached in the first place.
func Attach(live *Live, tail int) error {
	conn, err := Dial(live.State)
	if err != nil {
		return fmt.Errorf("connecting to server: %w", err)
	}
	defer conn.Close()

	if err := conn.Send(Request{Token: live.State.Token, Op: OpAttach, Tail: tail}); err != nil {
		return err
	}

	fmt.Printf("%sattached to %s%s  %s(Ctrl-C or Ctrl-D detaches; the server keeps running)%s\n",
		ui.Bold, live.State.Name, ui.Reset, ui.Dim, ui.Reset)

	streamDone := make(chan error, 1)
	go func() {
		out := bufio.NewWriter(os.Stdout)
		for {
			conn.SetDeadline(time.Time{}) // block indefinitely waiting for output
			resp, err := conn.Recv()
			if err != nil {
				out.Flush()
				streamDone <- err
				return
			}
			switch resp.Type {
			case RespLine:
				fmt.Fprintln(out, resp.Line)
				out.Flush()
			case RespExit:
				out.Flush()
				streamDone <- fmt.Errorf("server exited with code %d", resp.Code)
				return
			}
		}
	}()

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		br := bufio.NewReader(os.Stdin)
		for {
			line, err := br.ReadString('\n')
			line = strings.TrimRight(line, "\r\n")
			if line != "" {
				if serr := conn.Send(Request{Token: live.State.Token, Op: OpInput, Line: line}); serr != nil {
					return
				}
			}
			if err != nil {
				return // Ctrl-D / EOF detaches
			}
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	select {
	case err := <-streamDone:
		if err != nil && strings.Contains(err.Error(), "server exited") {
			fmt.Printf("\n%s%v%s\n", ui.Yellow, err, ui.Reset)
			return nil
		}
		return nil
	case <-sigCh:
	case <-inputDone:
	}
	// Close first so the streaming goroutine stops before the notice is
	// printed, rather than trailing a line after it.
	conn.Close()
	fmt.Printf("\n%sdetached; server is still running%s\n", ui.Dim, ui.Reset)
	return nil
}

// Stop asks a running server to shut down. When wait is true it blocks until
// the process is actually gone or the grace period expires.
func Stop(live *Live, timeoutSecs int, wait bool) error {
	if !live.Responsive {
		return fmt.Errorf("the supervisor for this server is not responding; use 'mcmgr server kill' to terminate it")
	}
	if _, err := Do(live.State, Request{Token: live.State.Token, Op: OpStop, Timeout: timeoutSecs}); err != nil {
		return err
	}
	if !wait {
		return nil
	}

	// The supervisor escalates to a kill at timeoutSecs, so allow a little
	// longer than that before giving up here.
	deadline := time.Now().Add(time.Duration(timeoutSecs+15) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		l, err := Probe(live.State.Dir)
		if err != nil {
			return err
		}
		if l == nil {
			return nil
		}
	}
	return fmt.Errorf("server did not stop within %ds", timeoutSecs+15)
}

// Kill terminates a server immediately. It first asks the supervisor, and
// falls back to signalling the recorded pids when the supervisor itself is
// wedged.
func Kill(live *Live) error {
	if live.Responsive {
		if _, err := Do(live.State, Request{Token: live.State.Token, Op: OpKill}); err == nil {
			for i := 0; i < 40; i++ {
				time.Sleep(100 * time.Millisecond)
				if l, _ := Probe(live.State.Dir); l == nil {
					return nil
				}
			}
		}
	}

	// Kill the JVM first so the supervisor cannot restart or outlive it, then
	// the supervisor itself. os.Process.Kill is SIGKILL on Unix and
	// TerminateProcess on Windows.
	var errs []string
	for _, pid := range []int{live.State.ChildPID, live.State.PID} {
		if pid <= 0 || !processAlive(pid) {
			continue
		}
		p, err := os.FindProcess(pid)
		if err != nil {
			errs = append(errs, fmt.Sprintf("pid %d: %v", pid, err))
			continue
		}
		if err := p.Kill(); err != nil {
			errs = append(errs, fmt.Sprintf("pid %d: %v", pid, err))
		}
	}
	_ = ClearState(live.State.Dir)
	if len(errs) > 0 {
		return fmt.Errorf("killing server: %s", strings.Join(errs, "; "))
	}
	return nil
}

// SendCommand runs a single console command and prints whatever the server
// logs in response. It returns once output goes quiet, so `mcmgr server cmd
// list` shows the player list rather than hanging.
func SendCommand(live *Live, command string) error {
	conn, err := Dial(live.State)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := conn.Send(Request{Token: live.State.Token, Op: OpAttach, Tail: -1}); err != nil {
		return err
	}
	if err := conn.Send(Request{Token: live.State.Token, Op: OpInput, Line: command}); err != nil {
		return err
	}

	const (
		quietPeriod = 900 * time.Millisecond
		maxWait     = 6 * time.Second
	)
	hardStop := time.Now().Add(maxWait)
	for {
		budget := time.Until(hardStop)
		if budget <= 0 {
			return nil
		}
		if budget > quietPeriod {
			budget = quietPeriod
		}
		conn.SetDeadline(time.Now().Add(budget))
		resp, err := conn.Recv()
		if err != nil {
			return nil // quiet period elapsed, or the connection ended
		}
		switch resp.Type {
		case RespLine:
			fmt.Println(resp.Line)
		case RespExit:
			return nil
		}
	}
}

// TailLog prints the last n lines of the captured console log, optionally
// following it. It reads the file directly, so it works on a stopped server
// too.
func TailLog(dir string, n int, follow bool) error {
	path := ConsoleLogFor(dir)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("no console log yet at %s; start the server once first", path)
	}
	if err != nil {
		return err
	}
	defer f.Close()

	offset, err := printTail(f, n)
	if err != nil {
		return err
	}
	if !follow {
		return nil
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for {
		select {
		case <-sigCh:
			return nil
		default:
		}

		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if fi.Size() < offset {
			// The log was rotated by a restart; follow the new file.
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
			offset = 0
		}
		if fi.Size() > offset {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				return err
			}
			br := bufio.NewReader(f)
			for {
				line, err := br.ReadString('\n')
				if line != "" {
					offset += int64(len(line))
					fmt.Fprint(out, line)
				}
				if err != nil {
					break
				}
			}
			out.Flush()
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// tailWindow is how much of the end of a log file is scanned for the last n
// lines; enough for a very long tail without reading gigabytes.
const tailWindow = 4 << 20

// printTail writes the last n lines of f and returns the resulting offset.
func printTail(f *os.File, n int) (int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := fi.Size()
	start := int64(0)
	if size > tailWindow {
		start = size - tailWindow
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return 0, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if n >= 0 && n < len(lines) {
		lines = lines[len(lines)-n:]
	}
	out := bufio.NewWriter(os.Stdout)
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
	out.Flush()
	return size, nil
}

// indentTail returns the last n lines of a file, indented, for error messages.
func indentTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return "       (no output captured in " + path + ")"
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i := range lines {
		lines[i] = "       " + lines[i]
	}
	return strings.Join(lines, "\n")
}
