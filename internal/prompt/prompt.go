// Package prompt asks the user for values mcmgr was not given on the command
// line.
//
// It wraps huh so the rest of the codebase never touches terminal modes: the
// library handles raw input, arrow keys, resizing and the differences between
// Windows consoles and Unix terminals. Styling is deliberately the plain base
// theme rather than anything decorative.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
)

// ErrAborted is returned when the user cancels a prompt with Ctrl-C or Esc.
var ErrAborted = errors.New("cancelled")

// ErrNotInteractive is returned when a value is needed but there is no
// terminal to ask on, such as in a script or CI job.
var ErrNotInteractive = errors.New("no terminal available to prompt on")

// Option is one entry in a Select.
type Option struct {
	// Label is shown to the user; Value is what gets stored.
	Label string
	Value string
}

// Opt builds an Option whose label and value are the same.
func Opt(v string) Option { return Option{Label: v, Value: v} }

// Accessible reports whether to use plain line-based prompts instead of the
// full-screen ones. It is for screen readers and for terminals that cannot
// handle cursor addressing, and it is what makes the prompt flow scriptable.
func Accessible() bool {
	v := strings.TrimSpace(os.Getenv("MCMGR_ACCESSIBLE"))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// Interactive reports whether prompting is possible: both stdin and stdout
// must be terminals, and MCMGR_NO_INPUT must not be set.
//
// Accessible mode overrides the terminal check, because it reads and writes
// plain lines and so works over a pipe.
func Interactive() bool {
	if os.Getenv("MCMGR_NO_INPUT") != "" {
		return false
	}
	if Accessible() {
		return true
	}
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

// isTerminal reports whether f is an interactive terminal.
//
// It asks the terminal driver rather than looking at the file mode, because a
// char-device check counts /dev/null as a terminal: "mcmgr run < /dev/null" in
// a terminal would then take the interactive path and detach immediately.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(f.Fd())
}

// theme is the plain theme; mcmgr does not decorate its prompts.
func theme() *huh.Theme { return huh.ThemeBase() }

// stdinReader is the single buffered view of stdin used by accessible mode.
var stdinReader = bufio.NewReader(os.Stdin)

// lineFeeder hands exactly one line to one accessible prompt and then reports
// EOF.
//
// Accessible prompts each build their own bufio.Scanner over whatever reader
// they are given, and a Scanner reads ahead in large chunks. Given stdin
// directly, the first prompt would swallow every remaining line and each later
// prompt would see EOF and silently keep its default. Rationing input a line
// at a time leaves the rest in stdinReader for the prompts that follow.
type lineFeeder struct {
	src  *bufio.Reader
	buf  []byte
	read bool
	// got records whether a line was actually available, which is how a
	// prompt tells "the user answered" from "input ran out".
	got bool
}

func (l *lineFeeder) Read(p []byte) (int, error) {
	if !l.read {
		line, err := l.src.ReadString('\n')
		l.read = true
		l.buf = []byte(line)
		l.got = len(line) > 0
		if len(line) == 0 && err != nil {
			return 0, err
		}
	}
	if len(l.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, l.buf)
	l.buf = l.buf[n:]
	return n, nil
}

// run executes an input field as a one-field form.
func run(field huh.Field) error { return runField(field, true) }

// runField executes a single field. readsInput must be false for fields that
// only display something, such as a note, since those consume no line.
//
// An accessible prompt that reaches end of input silently keeps its default,
// which would let a script or a Ctrl-D quietly accept settings nobody chose,
// and would spin the interactive editor forever. Checking whether a line was
// actually consumed turns that into an error instead.
func runField(field huh.Field, readsInput bool) error {
	form := huh.NewForm(huh.NewGroup(field)).
		WithTheme(theme()).
		WithShowHelp(true)

	var feeder *lineFeeder
	if Accessible() {
		feeder = &lineFeeder{src: stdinReader}
		form = form.WithAccessible(true).WithInput(feeder)
	}

	err := form.Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrAborted
	}
	if err != nil {
		return err
	}
	if feeder != nil && readsInput && !feeder.got {
		return fmt.Errorf("%w: input ended before the prompt was answered", ErrNotInteractive)
	}
	return nil
}

// Select asks the user to pick one option with the arrow keys and enter.
// def is the value selected when the prompt opens.
func Select(title, description string, options []Option, def string) (string, error) {
	if !Interactive() {
		return "", ErrNotInteractive
	}
	if len(options) == 0 {
		return "", fmt.Errorf("no options to choose from")
	}

	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o.Label, o.Value)
	}

	value := def
	if value == "" {
		value = options[0].Value
	}
	field := huh.NewSelect[string]().
		Title(title).
		Options(opts...).
		Value(&value)
	if description != "" {
		field = field.Description(description)
	}
	if err := run(field); err != nil {
		return "", err
	}
	return value, nil
}

// Confirm asks a yes/no question. Left/right or y/n choose, enter accepts.
func Confirm(title, description string, def bool) (bool, error) {
	if !Interactive() {
		return false, ErrNotInteractive
	}
	value := def
	field := huh.NewConfirm().
		Title(title).
		Affirmative("Yes").
		Negative("No").
		Value(&value)
	if description != "" {
		field = field.Description(description)
	}
	if err := run(field); err != nil {
		return false, err
	}
	return value, nil
}

// Text asks for a free-text value. validate may be nil.
func Text(title, description, def string, validate func(string) error) (string, error) {
	if !Interactive() {
		return "", ErrNotInteractive
	}
	value := def
	field := huh.NewInput().
		Title(title).
		Value(&value)
	if description != "" {
		field = field.Description(description)
	}
	if def != "" {
		field = field.Placeholder(def)
	}
	if validate != nil {
		field = field.Validate(func(s string) error {
			return validate(strings.TrimSpace(s))
		})
	}
	if err := run(field); err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = def
	}
	return value, nil
}

// Secret asks for a value that should not be echoed, such as an API key.
//
// Accessible mode cannot hide input, since it has no terminal to switch echo
// off on, so it falls back to a normal visible prompt and says so.
func Secret(title, description string) (string, error) {
	if !Interactive() {
		return "", ErrNotInteractive
	}
	echo := huh.EchoModePassword
	if Accessible() {
		echo = huh.EchoModeNormal
		if description != "" {
			description += "\n"
		}
		description += "Note: this will be visible as you type."
	}

	var value string
	field := huh.NewInput().
		Title(title).
		EchoMode(echo).
		Value(&value)
	if description != "" {
		field = field.Description(description)
	}
	if err := run(field); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

// Pause shows a message and waits for enter. It is used to make the user read
// a warning before continuing.
func Pause(title, description, label string) error {
	if !Interactive() {
		return ErrNotInteractive
	}
	if label == "" {
		label = "Continue"
	}
	return runField(huh.NewNote().
		Title(title).
		Description(description).
		Next(true).
		NextLabel(label), false)
}

// ValidateNonEmpty rejects blank input.
func ValidateNonEmpty(what string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("%s cannot be empty", what)
		}
		return nil
	}
}

// ValidateInt rejects anything that is not a whole number in [min, max].
func ValidateInt(what string, minValue, maxValue int) func(string) error {
	return func(s string) error {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("%s must be a whole number", what)
		}
		if n < minValue || n > maxValue {
			return fmt.Errorf("%s must be between %d and %d", what, minValue, maxValue)
		}
		return nil
	}
}

// ValidateName rejects names that would not work as a directory or registry
// key.
func ValidateName(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("name cannot be empty")
	}
	if strings.ContainsAny(s, `/\:*?"<>|`) {
		return errors.New(`name cannot contain / \ : * ? " < > |`)
	}
	if s == "." || s == ".." {
		return errors.New("name cannot be . or ..")
	}
	return nil
}

// MultiSelect asks the user to tick any number of options. Space toggles,
// enter accepts; selected lists the values ticked when the prompt opens. It
// returns the chosen values in the order the options were given.
func MultiSelect(title, description string, options []Option, selected []string) ([]string, error) {
	if !Interactive() {
		return nil, ErrNotInteractive
	}
	if len(options) == 0 {
		return nil, fmt.Errorf("no options to choose from")
	}

	pre := make(map[string]bool, len(selected))
	for _, s := range selected {
		pre[s] = true
	}
	if Accessible() {
		return multiSelectAccessible(title, description, options, pre)
	}
	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o.Label, o.Value).Selected(pre[o.Value])
	}

	var value []string
	field := huh.NewMultiSelect[string]().
		Title(title).
		Options(opts...).
		Filterable(true).
		Height(multiSelectHeight(len(options))).
		Value(&value)
	if description != "" {
		field = field.Description(description)
	}
	if err := run(field); err != nil {
		return nil, err
	}
	return value, nil
}

// multiSelectAccessible is the line-based form of MultiSelect: the options are
// numbered, each line typed toggles one, and an empty line or 0 finishes.
//
// It is hand-rolled rather than huh's own accessible multi-select because that
// one reads several lines from its input, and the one-line ration every other
// accessible prompt gets would end it after the first toggle.
func multiSelectAccessible(title, description string, options []Option, selected map[string]bool) ([]string, error) {
	fmt.Println(title)
	if description != "" {
		fmt.Println(description)
	}
	fmt.Println("Type a number to tick or untick it; press enter on an empty line when done.")
	for {
		for i, o := range options {
			mark := " "
			if selected[o.Value] {
				mark = "x"
			}
			fmt.Printf("%2d. [%s] %s\n", i+1, mark, o.Label)
		}
		fmt.Print("Toggle (1-", len(options), ") or enter to finish: ")
		line, err := stdinReader.ReadString('\n')
		if err != nil && line == "" {
			fmt.Println()
			return nil, fmt.Errorf("%w: input ended before the prompt was answered", ErrNotInteractive)
		}
		line = strings.TrimSpace(line)
		if line == "" || line == "0" {
			break
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(options) {
			fmt.Printf("enter a number between 1 and %d\n", len(options))
			continue
		}
		v := options[n-1].Value
		selected[v] = !selected[v]
	}
	var out []string
	for _, o := range options {
		if selected[o.Value] {
			out = append(out, o.Value)
		}
	}
	return out, nil
}

// multiSelectHeight keeps a long list from scrolling the title off screen. It
// is rows of options, and the field adds its own for the title and help.
func multiSelectHeight(n int) int {
	const max = 12
	if n > max {
		return max
	}
	return n
}
