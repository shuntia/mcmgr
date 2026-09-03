package cli

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mcmgr/internal/config"
	"mcmgr/internal/settings"
)

// TestDumpEditorView renders the editor against a real settings store so the
// layout can be eyeballed. It is skipped unless MCMGR_DUMP_VIEW is set.
func TestDumpEditorView(t *testing.T) {
	if os.Getenv("MCMGR_DUMP_VIEW") == "" {
		t.Skip("set MCMGR_DUMP_VIEW=1 to render the editor")
	}
	t.Setenv("MCMGR_HOME", t.TempDir())

	dir := t.TempDir()
	cfg := &config.Config{Name: "survival", Minecraft: "1.21.4", Loader: config.Fabric,
		Java: config.Java{Args: []string{"-Xmx4G"}}}
	cfg.SetDir(dir)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.properties"),
		[]byte("motd=A Minecraft Server\nwhite-list=false\ndifficulty=easy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := settings.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	keys := store.Keys()
	settings.SortKeys(keys)

	var rows []editorRow
	for _, k := range keys {
		v := store.Get(k)
		if k.Kind == settings.KindBool && v == "" {
			v = "false"
		}
		rows = append(rows, editorRow{key: k, original: v, value: v})
	}
	m := editorModel{
		title: "Settings for survival", rows: rows, width: 100, height: 26,
		validate: store.Validate,
		warnings: store.Warnings,
	}
	m.measure()
	m.moveTo(0)
	for i := range m.rows {
		if m.rows[i].key.Name == "properties.white-list" {
			m.moveTo(i)
			break
		}
	}
	// Toggle it on, so the warning panel is exercised too.
	m = press(m, tea.KeyMsg{Type: tea.KeyRight})
	os.Stdout.WriteString("\n" + m.View() + "\n")
}
