package cli

import (
	"slices"
	"testing"

	"mcmgr/internal/config"
	"mcmgr/internal/mods"
)

func TestAddonPicksDedupe(t *testing.T) {
	var p addonPicks
	if !p.add("Lithium") || p.add("lithium") || p.add("  ") {
		t.Error("add should accept a slug once, case-insensitively, and never a blank")
	}
	p.add("chunky")
	if !slices.Equal(p.slugs, []string{"lithium", "chunky"}) {
		t.Errorf("slugs = %v", p.slugs)
	}
	if !p.has("LITHIUM") || p.has("spark") {
		t.Error("has should match case-insensitively")
	}
}

// TestNewFlagsForAddons covers the unattended form: --recommended selects the
// pre-ticked set and --mod adds to it, in the order given, without repeats.
func TestNewFlagsForAddons(t *testing.T) {
	cmd := &Command{Name: "new"}
	fs, g := newFlags(cmd, "mcmgr new")
	var v newFlagValues
	v.register(fs)
	if _, err := parse(fs, g, []string{"smp", "--recommended", "--mod", "chunky", "--mod", "lithium", "--mod", "chunky"}); err != nil {
		t.Fatal(err)
	}
	if !*v.recommended {
		t.Error("--recommended not parsed")
	}
	var picks addonPicks
	for _, slug := range recommendedSlugs(mods.RecommendedDefaults(config.Fabric)) {
		picks.add(slug)
	}
	for _, slug := range v.addons {
		picks.add(slug)
	}
	want := append(recommendedSlugs(mods.RecommendedDefaults(config.Fabric)), "chunky")
	if !slices.Equal(picks.slugs, want) {
		t.Errorf("picks = %v, want %v", picks.slugs, want)
	}
}
