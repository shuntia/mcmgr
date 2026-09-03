package warn

import "testing"

// TestWhitelistWarnings covers both directions: enabling it locks people out
// (including you), disabling it lets everyone in.
func TestWhitelistWarnings(t *testing.T) {
	on := ForProperty("white-list", "true", Context{Old: "false"})
	if len(on) != 1 || on[0].Level != Caution {
		t.Errorf("enabling the whitelist should be a caution, got %+v", on)
	}

	off := ForProperty("white-list", "false", Context{Old: "true"})
	if len(off) != 1 || off[0].Level != Danger {
		t.Errorf("disabling the whitelist should be a danger, got %+v", off)
	}

	// Writing false over false is not a change worth warning about.
	if ws := ForProperty("white-list", "false", Context{Old: "false"}); len(ws) != 0 {
		t.Errorf("no-op should not warn, got %+v", ws)
	}
}

func TestOnlineModeOffIsDanger(t *testing.T) {
	ws := ForProperty("online-mode", "false", Context{Old: "true"})
	if len(ws) != 1 || ws[0].Level != Danger {
		t.Errorf("online-mode=false should be a danger, got %+v", ws)
	}
	if ws := ForProperty("online-mode", "true", Context{Old: "false"}); len(ws) != 0 {
		t.Errorf("turning verification on should not warn, got %+v", ws)
	}
}
