package cli

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mcmgr/internal/settings"
	"mcmgr/internal/warn"
)

// newTestEditor builds an editor over a fixed set of rows: a bool, a choice
// list, a free-text value, an int and a locked row.
func newTestEditor(t *testing.T) editorModel {
	t.Helper()

	rows := []editorRow{
		{key: settings.Key{Name: "minecraft", Scope: settings.Manifest, Kind: settings.KindString},
			original: "1.21.4", value: "1.21.4"},
		{key: settings.Key{Name: "loader", Scope: settings.Manifest, Kind: settings.KindString,
			Choices: []string{"vanilla", "fabric", "quilt", "paper", "neoforge"}},
			original: "fabric", value: "fabric"},
		{key: settings.Key{Name: "defaults.allow_beta", Scope: settings.Manifest, Kind: settings.KindBool},
			original: "false", value: "false"},
		{key: settings.Key{Name: "properties.motd", Scope: settings.Properties, Kind: settings.KindString},
			original: "Hello", value: "Hello"},
		{key: settings.Key{Name: "properties.max-players", Scope: settings.Properties, Kind: settings.KindInt},
			original: "20", value: "20"},
		{key: settings.Key{Name: "properties.white-list", Scope: settings.Properties, Kind: settings.KindBool},
			original: "false", value: "false", locked: true, lockReason: "the server is running"},
	}

	// Validation mirrors what the real store does for these kinds.
	validate := func(k settings.Key, v string) error {
		if k.Kind == settings.KindInt && v != "" && strings.TrimLeft(v, "0123456789") != "" {
			return fmt.Errorf("%s must be a whole number", k.Name)
		}
		return nil
	}

	m := editorModel{
		title: "Settings", rows: rows, width: 80, height: 24,
		validate: validate,
		warnings: func(settings.Key, string) []warn.Warning { return nil },
	}
	m.measure()
	m.moveTo(0)
	return m
}

// press sends one key and returns the updated model.
func press(m editorModel, key tea.KeyMsg) editorModel {
	next, _ := m.Update(key)
	return next.(editorModel)
}

func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// rowByName finds a row so tests do not depend on list ordering.
func rowByName(m editorModel, name string) *editorRow {
	for i := range m.rows {
		if m.rows[i].key.Name == name {
			return &m.rows[i]
		}
	}
	return nil
}

func cursorOn(m *editorModel, name string) {
	for i := range m.rows {
		if m.rows[i].key.Name == name {
			m.moveTo(i)
			return
		}
	}
}

// TestLeftRightTogglesBool is the core of the request: a yes/no setting flips
// where it sits, with no submenu.
func TestLeftRightTogglesBool(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "defaults.allow_beta")

	m = press(m, key(tea.KeyRight))
	if got := rowByName(m, "defaults.allow_beta").value; got != "true" {
		t.Errorf("right on a bool gave %q, want true", got)
	}
	m = press(m, key(tea.KeyRight))
	if got := rowByName(m, "defaults.allow_beta").value; got != "false" {
		t.Errorf("right again should wrap back to false, got %q", got)
	}
	m = press(m, key(tea.KeyLeft))
	if got := rowByName(m, "defaults.allow_beta").value; got != "true" {
		t.Errorf("left on a bool gave %q, want true", got)
	}
	if !rowByName(m, "defaults.allow_beta").changed() {
		t.Error("the row should report itself as changed")
	}
}

// TestLeftRightCyclesChoices covers enumerated values with more than two
// options, including wrapping in both directions.
func TestLeftRightCyclesChoices(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "loader")

	// fabric is index 1 of [vanilla fabric quilt paper neoforge].
	m = press(m, key(tea.KeyRight))
	if got := rowByName(m, "loader").value; got != "quilt" {
		t.Errorf("right gave %q, want quilt", got)
	}
	m = press(m, key(tea.KeyLeft))
	m = press(m, key(tea.KeyLeft))
	if got := rowByName(m, "loader").value; got != "vanilla" {
		t.Errorf("two lefts gave %q, want vanilla", got)
	}
	// Wrapping backwards off the start lands on the last option.
	m = press(m, key(tea.KeyLeft))
	if got := rowByName(m, "loader").value; got != "neoforge" {
		t.Errorf("left from the first option gave %q, want neoforge", got)
	}
	// And forwards off the end wraps to the first.
	m = press(m, key(tea.KeyRight))
	if got := rowByName(m, "loader").value; got != "vanilla" {
		t.Errorf("right from the last option gave %q, want vanilla", got)
	}
}

// TestTypingEditsInPlace is the other half of the request: text is edited in
// the row by typing and backspace.
func TestTypingEditsInPlace(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "minecraft")

	// The caret starts at the end, so typing appends.
	m = press(m, runes("5"))
	if got := rowByName(m, "minecraft").value; got != "1.21.45" {
		t.Errorf("typing gave %q, want 1.21.45", got)
	}

	// Backspace twice removes the typed digit and the one before it.
	m = press(m, key(tea.KeyBackspace))
	m = press(m, key(tea.KeyBackspace))
	if got := rowByName(m, "minecraft").value; got != "1.21." {
		t.Errorf("after two backspaces got %q, want 1.21.", got)
	}

	m = press(m, runes("5"))
	if got := rowByName(m, "minecraft").value; got != "1.21.5" {
		t.Errorf("final value %q, want 1.21.5", got)
	}
}

// TestTextCaretMovement checks that left/right move within a text value rather
// than cycling it, and that typing lands at the caret.
func TestTextCaretMovement(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "minecraft")
	before := rowByName(m, "minecraft").value

	m = press(m, key(tea.KeyLeft))
	if got := rowByName(m, "minecraft").value; got != before {
		t.Errorf("left on a text row changed the value to %q; it should only move the caret", got)
	}
	if got := rowByName(m, "minecraft").caret; got != len(before)-1 {
		t.Errorf("caret = %d, want %d", got, len(before)-1)
	}

	// Insert in the middle, not at the end.
	m = press(m, runes("9"))
	if got := rowByName(m, "minecraft").value; got != "1.21.94" {
		t.Errorf("insert at caret gave %q, want 1.21.94", got)
	}

	// The caret cannot run off either end.
	for i := 0; i < 20; i++ {
		m = press(m, key(tea.KeyLeft))
	}
	if got := rowByName(m, "minecraft").caret; got != 0 {
		t.Errorf("caret = %d after many lefts, want 0", got)
	}
	for i := 0; i < 40; i++ {
		m = press(m, key(tea.KeyRight))
	}
	if got, want := rowByName(m, "minecraft").caret, len([]rune(rowByName(m, "minecraft").value)); got != want {
		t.Errorf("caret = %d after many rights, want %d", got, want)
	}
}

func TestSpaceIsTypedIntoText(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "properties.motd")

	// Both spellings a terminal might send.
	m = press(m, key(tea.KeySpace))
	m = press(m, runes("there"))
	if got := rowByName(m, "properties.motd").value; got != "Hello there" {
		t.Errorf("motd = %q, want %q", got, "Hello there")
	}
}

// TestBackspaceOnEnumStepsBack keeps backspace meaningful on a row where there
// is no text to delete.
func TestBackspaceOnEnumStepsBack(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "defaults.allow_beta")

	m = press(m, key(tea.KeyBackspace))
	if got := rowByName(m, "defaults.allow_beta").value; got != "true" {
		t.Errorf("backspace on a bool gave %q, want it to step to true", got)
	}
}

// TestTypingOnEnumJumpsToChoice makes typing useful on a fixed set instead of
// inserting characters that could never be valid.
func TestTypingOnEnumJumpsToChoice(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "loader")

	m = press(m, runes("p"))
	if got := rowByName(m, "loader").value; got != "paper" {
		t.Errorf("typing p gave %q, want paper", got)
	}
	m = press(m, runes("v"))
	if got := rowByName(m, "loader").value; got != "vanilla" {
		t.Errorf("typing v gave %q, want vanilla", got)
	}
	// A letter matching nothing leaves the value alone.
	m = press(m, runes("z"))
	if got := rowByName(m, "loader").value; got != "vanilla" {
		t.Errorf("an unmatched letter changed the value to %q", got)
	}
}

// TestLockedRowsRejectEdits covers server.properties while the server runs.
func TestLockedRowsRejectEdits(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "properties.white-list")

	for _, k := range []tea.KeyMsg{key(tea.KeyRight), key(tea.KeyLeft), key(tea.KeyBackspace), runes("t")} {
		m = press(m, k)
	}
	row := rowByName(m, "properties.white-list")
	if row.value != "false" || row.changed() {
		t.Errorf("a locked row was edited to %q", row.value)
	}
	if m.status == "" {
		t.Error("editing a locked row should explain why it did not work")
	}
}

// TestInvalidValueBlocksSave stops the editor writing something the store
// would reject, and points at the offending row.
func TestInvalidValueBlocksSave(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "properties.max-players")

	m = press(m, runes("x"))
	if rowByName(m, "properties.max-players").invalid == nil {
		t.Fatal("a non-numeric int should be marked invalid")
	}

	// Move away, then try to save from elsewhere.
	cursorOn(&m, "minecraft")
	m = press(m, key(tea.KeyEnter))
	if m.save {
		t.Error("saving should be refused while a row is invalid")
	}
	if m.rows[m.cursor].key.Name != "properties.max-players" {
		t.Errorf("cursor should jump to the invalid row, it is on %s", m.rows[m.cursor].key.Name)
	}
	if m.status == "" {
		t.Error("the reason should be shown")
	}

	// Fixing it lets the save through.
	m = press(m, key(tea.KeyBackspace))
	m = press(m, key(tea.KeyEnter))
	if !m.save {
		t.Error("saving should be allowed once the value is valid")
	}
}

func TestEnterSavesAndEscCancels(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "defaults.allow_beta")
	m = press(m, key(tea.KeyRight))

	saved := press(m, key(tea.KeyEnter))
	if !saved.save || saved.cancelled {
		t.Error("enter should mark the model for saving")
	}

	cancelled := press(m, key(tea.KeyEsc))
	if !cancelled.cancelled || cancelled.save {
		t.Error("esc should cancel")
	}
}

func TestClearAndRevert(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "properties.motd")

	m = press(m, key(tea.KeyCtrlU))
	if got := rowByName(m, "properties.motd").value; got != "" {
		t.Errorf("ctrl+u gave %q, want an empty value", got)
	}
	m = press(m, runes("New"))
	m = press(m, key(tea.KeyCtrlR))
	row := rowByName(m, "properties.motd")
	if row.value != "Hello" || row.changed() {
		t.Errorf("ctrl+r gave %q, want the original Hello", row.value)
	}
}

// TestScrollingKeepsCursorVisible checks the window follows the cursor on a
// list taller than the terminal.
func TestScrollingKeepsCursorVisible(t *testing.T) {
	m := newTestEditor(t)
	m.height = 12 // room for only a few rows

	m.moveTo(len(m.rows) - 1)
	visible := m.visibleRows()
	if m.cursor < m.top || m.cursor >= m.top+visible {
		t.Errorf("cursor %d outside the window [%d,%d)", m.cursor, m.top, m.top+visible)
	}

	m.moveTo(0)
	if m.top != 0 {
		t.Errorf("top = %d after moving to the first row, want 0", m.top)
	}

	// Movement is clamped rather than wrapping past the ends.
	m.moveTo(-5)
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want it clamped to 0", m.cursor)
	}
	m.moveTo(len(m.rows) + 50)
	if m.cursor != len(m.rows)-1 {
		t.Errorf("cursor = %d, want it clamped to the last row", m.cursor)
	}
}

// TestViewRenders is a smoke test that the view builds without panicking and
// shows the essentials.
func TestViewRenders(t *testing.T) {
	m := newTestEditor(t)
	cursorOn(&m, "defaults.allow_beta")
	m = press(m, key(tea.KeyRight))

	out := m.View()
	for _, want := range []string{"Settings", "defaults.allow_beta", "loader", "unsaved change"} {
		if !strings.Contains(out, want) {
			t.Errorf("view is missing %q:\n%s", want, out)
		}
	}
	// The key hints have to be discoverable, since nothing else explains them.
	for _, want := range []string{"←→", "enter", "esc"} {
		if !strings.Contains(out, want) {
			t.Errorf("view is missing the hint %q", want)
		}
	}
}
