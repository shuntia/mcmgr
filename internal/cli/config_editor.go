package cli

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mcmgr/internal/settings"
	"mcmgr/internal/ui"
	"mcmgr/internal/warn"
)

// editorRow is one setting in the in-place editor.
type editorRow struct {
	key      settings.Key
	original string
	value    string
	// caret is the insertion point for text rows, as a rune index.
	caret int
	// locked rows are shown but cannot be changed, e.g. server.properties
	// while the server is running.
	locked     bool
	lockReason string
	// invalid holds a validation failure for the current value.
	invalid error
}

// changed reports whether the row differs from what was loaded.
func (r *editorRow) changed() bool { return r.value != r.original }

// enum reports whether the row's value is chosen from a fixed set, which is
// what left/right cycles through.
func (r *editorRow) enum() bool {
	return r.key.Kind == settings.KindBool || len(r.key.Choices) > 0
}

// options returns the values left/right cycles between.
func (r *editorRow) options() []string {
	if r.key.Kind == settings.KindBool {
		return []string{"false", "true"}
	}
	return r.key.Choices
}

// cycle moves an enum row to the next or previous value, wrapping around.
func (r *editorRow) cycle(delta int) {
	opts := r.options()
	if len(opts) == 0 {
		return
	}
	at := 0
	for i, o := range opts {
		if strings.EqualFold(o, r.value) {
			at = i
			break
		}
	}
	r.value = opts[((at+delta)%len(opts)+len(opts))%len(opts)]
}

// editorModel is the settings editor: a single list where values are changed
// where they sit, rather than by opening a prompt per setting.
type editorModel struct {
	title string
	rows  []editorRow

	cursor int
	top    int
	width  int
	height int
	// nameWidth is the widest key name, so values line up in one column.
	nameWidth int

	status    string
	save      bool
	cancelled bool

	// validate and warnings come from the settings store, so the model itself
	// stays free of any dependency on it.
	validate func(settings.Key, string) error
	warnings func(settings.Key, string) []warn.Warning
}

// measure sizes the name column to the widest key, so values line up.
func (m *editorModel) measure() {
	for i := range m.rows {
		if n := len(m.rows[i].key.Name); n > m.nameWidth {
			m.nameWidth = n
		}
	}
}

func (m editorModel) Init() tea.Cmd { return nil }

// visibleRows is how many settings fit on screen, leaving room for the header,
// the warning area and the key hints.
func (m editorModel) visibleRows() int {
	n := m.height - 8
	if n < 3 {
		n = 3
	}
	if n > len(m.rows) {
		n = len(m.rows)
	}
	return n
}

func (m *editorModel) moveTo(i int) {
	if i < 0 {
		i = 0
	}
	if i >= len(m.rows) {
		i = len(m.rows) - 1
	}
	m.cursor = i
	m.status = ""

	// Keep the cursor inside the scrolled window.
	visible := m.visibleRows()
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+visible {
		m.top = m.cursor - visible + 1
	}
	if m.top < 0 {
		m.top = 0
	}

	// A text caret always starts at the end of the value.
	row := &m.rows[m.cursor]
	row.caret = len([]rune(row.value))
}

func (m editorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.moveTo(m.cursor)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m editorModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.rows) == 0 {
		return m, tea.Quit
	}
	row := &m.rows[m.cursor]

	switch msg.String() {
	case "ctrl+c", "esc":
		m.cancelled = true
		return m, tea.Quit

	case "enter":
		if bad := m.firstInvalid(); bad >= 0 {
			m.moveTo(bad)
			m.status = fmt.Sprintf("cannot save: %v", m.rows[bad].invalid)
			return m, nil
		}
		m.save = true
		return m, tea.Quit

	case "up":
		m.moveTo(m.cursor - 1)
	case "down":
		m.moveTo(m.cursor + 1)
	case "pgup":
		m.moveTo(m.cursor - m.visibleRows())
	case "pgdown":
		m.moveTo(m.cursor + m.visibleRows())
	case "home":
		m.moveTo(0)
	case "end":
		m.moveTo(len(m.rows) - 1)

	case "left":
		if row.locked {
			m.status = row.lockReason
			break
		}
		if row.enum() {
			row.cycle(-1)
			m.revalidate(row)
		} else if row.caret > 0 {
			row.caret--
		}

	case "right":
		if row.locked {
			m.status = row.lockReason
			break
		}
		if row.enum() {
			row.cycle(1)
			m.revalidate(row)
		} else if row.caret < len([]rune(row.value)) {
			row.caret++
		}

	case "backspace":
		if row.locked {
			m.status = row.lockReason
			break
		}
		if row.enum() {
			// Nothing to delete; treat it as stepping back a value.
			row.cycle(-1)
			m.revalidate(row)
			break
		}
		if row.caret > 0 {
			r := []rune(row.value)
			row.value = string(append(r[:row.caret-1], r[row.caret:]...))
			row.caret--
			m.revalidate(row)
		}

	case "delete":
		if row.locked || row.enum() {
			break
		}
		r := []rune(row.value)
		if row.caret < len(r) {
			row.value = string(append(r[:row.caret], r[row.caret+1:]...))
			m.revalidate(row)
		}

	case "ctrl+u":
		if row.locked || row.enum() {
			break
		}
		row.value, row.caret = "", 0
		m.revalidate(row)

	case "ctrl+r":
		// Put this one row back to the value it was loaded with.
		row.value = row.original
		row.caret = len([]rune(row.value))
		m.revalidate(row)
		m.status = "reverted " + row.key.Name

	case " ":
		// Depending on the terminal a space arrives as its own key type rather
		// than a rune, and values like a motd need it.
		if row.locked || row.enum() {
			break
		}
		m.insert(row, " ")

	default:
		// Anything printable edits the value where it sits.
		if msg.Type != tea.KeyRunes || row.locked {
			break
		}
		if row.enum() {
			// On a fixed set, a keystroke jumps to the matching option
			// instead of inserting characters that could never be valid.
			m.jumpToChoice(row, string(msg.Runes))
			m.revalidate(row)
			break
		}
		m.insert(row, string(msg.Runes))
	}
	return m, nil
}

// insert places text at the caret and moves it along.
func (m *editorModel) insert(row *editorRow, text string) {
	r := []rune(row.value)
	if row.caret > len(r) {
		row.caret = len(r)
	}
	row.value = string(r[:row.caret]) + text + string(r[row.caret:])
	row.caret += len([]rune(text))
	m.revalidate(row)
}

// jumpToChoice selects the first option starting with the typed text.
func (m *editorModel) jumpToChoice(row *editorRow, typed string) {
	for _, o := range row.options() {
		if strings.HasPrefix(strings.ToLower(o), strings.ToLower(typed)) {
			row.value = o
			return
		}
	}
}

// firstInvalid returns the index of the first row that would not save, or -1.
func (m editorModel) firstInvalid() int {
	for i := range m.rows {
		if m.rows[i].invalid != nil {
			return i
		}
	}
	return -1
}

// revalidate re-checks a row against the store after every edit, so an
// invalid value is visible immediately rather than at save time.
func (m *editorModel) revalidate(row *editorRow) {
	if m.validate != nil {
		row.invalid = m.validate(row.key, row.value)
	}
}

func (m editorModel) View() string {
	var b strings.Builder

	changes := 0
	for i := range m.rows {
		if m.rows[i].changed() {
			changes++
		}
	}

	fmt.Fprintf(&b, "%s%s%s", ui.Bold, m.title, ui.Reset)
	if changes > 0 {
		fmt.Fprintf(&b, "   %s%d unsaved change(s)%s", ui.Yellow, changes, ui.Reset)
	}
	b.WriteString("\n\n")

	visible := m.visibleRows()
	end := m.top + visible
	if end > len(m.rows) {
		end = len(m.rows)
	}

	var lastScope settings.Scope = -1
	for i := m.top; i < end; i++ {
		row := m.rows[i]
		if row.key.Scope != lastScope {
			lastScope = row.key.Scope
			fmt.Fprintf(&b, "  %s%s%s\n", ui.Dim, row.key.Scope, ui.Reset)
		}
		b.WriteString(m.renderRow(i))
		b.WriteByte('\n')
	}

	if m.top > 0 || end < len(m.rows) {
		fmt.Fprintf(&b, "  %s(%d-%d of %d)%s\n", ui.Dim, m.top+1, end, len(m.rows), ui.Reset)
	}

	b.WriteString("\n")
	b.WriteString(m.renderFooter())
	return b.String()
}

func (m editorModel) renderRow(i int) string {
	row := m.rows[i]
	selected := i == m.cursor

	marker := "  "
	if selected {
		marker = ui.Bold + "> " + ui.Reset
	}

	name := fmt.Sprintf("%-*s", m.nameWidth, row.key.Name)
	if row.changed() {
		name = ui.Yellow + name + ui.Reset
	} else if row.locked {
		name = ui.Dim + name + ui.Reset
	}

	value := m.renderValue(row, selected)

	hint := ""
	if selected && !row.locked {
		if row.enum() {
			hint = ui.Dim + "  ← →" + ui.Reset
		} else {
			hint = ui.Dim + "  type to edit" + ui.Reset
		}
	}
	if row.locked {
		hint = ui.Dim + "  locked" + ui.Reset
	}
	return "  " + marker + name + " " + value + hint
}

// renderValue draws the value, with a visible caret on the selected text row.
func (m editorModel) renderValue(row editorRow, selected bool) string {
	shown := row.value
	if row.key.Kind == settings.KindSecret && shown != "" {
		shown = strings.Repeat("*", len([]rune(shown)))
	}

	// Keep a long value on one line, so the list never reflows as the cursor
	// moves. The selected row is left intact so it can still be edited.
	if !selected {
		if room := m.width - m.nameWidth - 20; room > 12 && len([]rune(shown)) > room {
			shown = string([]rune(shown)[:room-1]) + "…"
		}
	}

	if row.invalid != nil {
		return ui.Red + orPlaceholder(shown) + ui.Reset
	}
	if !selected || row.locked || row.enum() {
		if shown == "" {
			return ui.Dim + "(unset)" + ui.Reset
		}
		return shown
	}

	// A block caret shows where typing will land.
	r := []rune(shown)
	caret := row.caret
	if caret > len(r) {
		caret = len(r)
	}
	before, after := string(r[:caret]), string(r[caret:])
	under := " "
	if len(after) > 0 {
		under = string([]rune(after)[0])
		after = string([]rune(after)[1:])
	}
	return before + "\x1b[7m" + under + "\x1b[27m" + after
}

func orPlaceholder(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func (m editorModel) renderFooter() string {
	var b strings.Builder

	if m.status != "" {
		fmt.Fprintf(&b, "  %s%s%s\n\n", ui.Red, m.status, ui.Reset)
	} else if len(m.rows) > 0 {
		row := m.rows[m.cursor]
		switch {
		case row.invalid != nil:
			fmt.Fprintf(&b, "  %s%v%s\n\n", ui.Red, row.invalid, ui.Reset)
		case row.changed() && m.warnings != nil:
			// Consequences appear as the value is changed, not after saving.
			ws := m.warnings(row.key, row.value)
			if len(ws) > 0 {
				w := ws[0]
				label, colour := "caution:", ui.Yellow
				if w.Level == warn.Danger {
					label, colour = "warning:", ui.Red
				}
				fmt.Fprintf(&b, "  %s%s%s %s\n", colour, label, ui.Reset, w.Title)
				for _, line := range strings.Split(w.Body, "\n") {
					fmt.Fprintf(&b, "    %s%s%s\n", ui.Dim, line, ui.Reset)
				}
				b.WriteByte('\n')
			} else if row.key.Help != "" {
				fmt.Fprintf(&b, "  %s%s%s\n\n", ui.Dim, row.key.Help, ui.Reset)
			}
		case row.key.Help != "":
			fmt.Fprintf(&b, "  %s%s%s\n\n", ui.Dim, row.key.Help, ui.Reset)
		default:
			b.WriteString("\n")
		}
	}

	fmt.Fprintf(&b, "  %s↑↓ move   ←→ change   type/backspace edit   ctrl+r revert   enter save   esc cancel%s",
		ui.Dim, ui.Reset)
	return b.String()
}

// runConfigEditorTUI is the in-place settings editor. Values are changed where
// they sit: left/right cycles anything with a fixed set of values, and text is
// typed straight into the row.
func runConfigEditorTUI(store *settings.Store, title string) error {
	keys := store.Keys()
	settings.SortKeys(keys)

	rows := make([]editorRow, 0, len(keys))
	for _, k := range keys {
		row := editorRow{key: k, original: store.Get(k), value: store.Get(k)}
		if k.Kind == settings.KindBool && row.value == "" {
			row.value = "false"
		}
		row.original = row.value
		if k.Scope == settings.Properties && store.Ctx.Running {
			row.locked = true
			row.lockReason = "the server is running; stop it to edit " + store.Props.Path()
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		ui.Info("nothing to configure here")
		return nil
	}

	m := editorModel{
		title: title, rows: rows, width: 80, height: 24,
		// The model validates and explains through these, so it needs no
		// dependency on the settings store itself.
		validate: store.Validate,
		warnings: store.Warnings,
	}
	m.measure()
	m.moveTo(0)

	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	result, ok := final.(editorModel)
	if !ok || result.cancelled {
		ui.Info("cancelled; nothing was changed")
		return nil
	}

	return applyEditorRows(store, result.rows)
}

// applyEditorRows writes back everything the editor changed, confirming any
// change severe enough to warrant it.
func applyEditorRows(store *settings.Store, rows []editorRow) error {
	var changed []editorRow
	for _, r := range rows {
		if r.changed() && !r.locked {
			changed = append(changed, r)
		}
	}
	if len(changed) == 0 {
		ui.Info("no changes")
		return nil
	}

	// Every dangerous change is shown under the setting it belongs to, so the
	// confirmation is about something concrete rather than "N changes".
	var dangerous []string
	for _, r := range changed {
		var ds []warn.Warning
		for _, w := range store.Warnings(r.key, r.value) {
			if w.Level == warn.Danger {
				ds = append(ds, w)
			}
		}
		if len(ds) == 0 {
			continue
		}
		dangerous = append(dangerous, r.key.Name)
		fmt.Fprintf(os.Stderr, "\n%s%s = %s%s", ui.Bold, r.key.Name, orPlaceholder(r.value), ui.Reset)
		warn.PrintAll(ds)
	}
	if len(dangerous) > 0 {
		question := fmt.Sprintf("Apply %s, including %s?", plural(len(changed), "change"), strings.Join(dangerous, ", "))
		if len(changed) == 1 {
			question = fmt.Sprintf("Apply %s = %s anyway?", changed[0].key.Name, orPlaceholder(changed[0].value))
		}
		ok, err := confirmDanger(question)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("cancelled; nothing was changed")
		}
	}

	for _, r := range changed {
		if err := store.Set(r.key, r.value); err != nil {
			return fmt.Errorf("%s: %w", r.key.Name, err)
		}
	}
	if err := store.Save(); err != nil {
		return err
	}

	for _, r := range changed {
		shown := r.value
		if r.key.Kind == settings.KindSecret {
			shown = "(set)"
		}
		ui.Ok("%s = %s", r.key.Name, orPlaceholder(shown))
	}
	if store.Ctx.Running {
		ui.Info("       the server is running; changes take effect on the next start")
	}
	return nil
}
