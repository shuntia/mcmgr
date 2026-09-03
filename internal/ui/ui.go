// Package ui holds terminal output helpers: colour handling, status lines and
// the small progress bar used while downloading.
package ui

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ANSI escapes. All are empty strings when colour is disabled, so they can be
// interpolated unconditionally.
var (
	Bold   = ""
	Dim    = ""
	Red    = ""
	Green  = ""
	Yellow = ""
	Blue   = ""
	Cyan   = ""
	Reset  = ""
)

// Quiet suppresses Info/Step output. Set from the global --quiet flag.
var Quiet bool

func init() {
	if colorEnabled() {
		Bold, Dim = "\x1b[1m", "\x1b[2m"
		Red, Green, Yellow = "\x1b[31m", "\x1b[32m", "\x1b[33m"
		Blue, Cyan = "\x1b[34m", "\x1b[36m"
		Reset = "\x1b[0m"
	}
}

func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("MCMGR_COLOR") == "always" {
		return true
	}
	fi, err := os.Stdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return enableVT()
}

// Step prints a progress line, e.g. "  -> downloading fabric-api".
func Step(format string, a ...any) {
	if Quiet {
		return
	}
	fmt.Printf("%s->%s %s\n", Cyan, Reset, fmt.Sprintf(format, a...))
}

// Info prints a plain informational line.
func Info(format string, a ...any) {
	if Quiet {
		return
	}
	fmt.Println(fmt.Sprintf(format, a...))
}

// Ok prints a success line.
func Ok(format string, a ...any) {
	if Quiet {
		return
	}
	fmt.Printf("%s+%s %s\n", Green, Reset, fmt.Sprintf(format, a...))
}

// Warn prints a warning to stderr; it is not suppressed by --quiet.
func Warn(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%swarning:%s %s\n", Yellow, Reset, fmt.Sprintf(format, a...))
}

// Progress renders a single-line download progress bar.
type Progress struct {
	label   string
	total   int64
	done    int64
	last    time.Time
	started time.Time
	mu      sync.Mutex
	active  bool
}

// NewProgress returns a progress bar for total bytes. A total <= 0 renders a
// byte counter instead of a bar. It is inert when output is not a terminal.
func NewProgress(label string, total int64) *Progress {
	return &Progress{
		label:   label,
		total:   total,
		started: time.Now(),
		active:  !Quiet && Reset != "",
	}
}

// Write implements io.Writer so a Progress can be used as a tee target.
func (p *Progress) Write(b []byte) (int, error) {
	p.Add(int64(len(b)))
	return len(b), nil
}

// Add records n more transferred bytes and repaints at most every 60ms.
func (p *Progress) Add(n int64) {
	if !p.active {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done += n
	if time.Since(p.last) < 60*time.Millisecond {
		return
	}
	p.last = time.Now()
	p.paint()
}

func (p *Progress) paint() {
	const width = 24
	if p.total > 0 {
		frac := float64(p.done) / float64(p.total)
		if frac > 1 {
			frac = 1
		}
		filled := int(frac * width)
		bar := strings.Repeat("=", filled) + strings.Repeat(" ", width-filled)
		fmt.Printf("\r  %s [%s] %3.0f%% %s  ", p.label, bar, frac*100, Bytes(p.total))
	} else {
		fmt.Printf("\r  %s %s  ", p.label, Bytes(p.done))
	}
}

// Done clears the progress line.
func (p *Progress) Done() {
	if !p.active {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Printf("\r%s\r", strings.Repeat(" ", 78))
}

// Bytes formats a byte count in human-readable units.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 3; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// Duration formats a duration compactly, e.g. "3d 4h", "12m 5s".
func Duration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// Table renders aligned columns padded to the widest cell.
func Table(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) && len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	var b strings.Builder
	for _, r := range rows {
		for i, c := range r {
			if i == len(r)-1 {
				b.WriteString(c)
			} else {
				b.WriteString(c + strings.Repeat(" ", widths[i]-len(c)+2))
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
