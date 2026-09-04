package mods

import (
	"slices"
	"testing"
)

func TestIsRelease(t *testing.T) {
	for _, id := range []string{"1.21", "1.21.4", "1.20.1", "26.1"} {
		if !IsRelease(id) {
			t.Errorf("%q should be a release", id)
		}
	}
	for _, id := range []string{"24w33a", "1.21.5-pre1", "1.21.5-rc1", "", "1.21.x", "b1.7.3"} {
		if IsRelease(id) {
			t.Errorf("%q must not count as a release", id)
		}
	}
}

func TestCompareMCIsNumeric(t *testing.T) {
	if CompareMC("1.21.10", "1.21.9") <= 0 {
		t.Error("1.21.10 must sort after 1.21.9")
	}
	if CompareMC("1.21", "1.21.0") != 0 {
		t.Error("1.21 and 1.21.0 are the same version")
	}
	if CompareMC("1.20.6", "1.21") >= 0 {
		t.Error("1.20.6 must sort before 1.21")
	}
}

func TestSortReleasesAndNearest(t *testing.T) {
	ids := []string{"1.21.1", "24w33a", "1.21.5", "1.20.6", "1.21.1", "1.21.5-pre1", "1.21.10"}
	got := SortReleases(ids)
	want := []string{"1.21.10", "1.21.5", "1.21.1", "1.20.6"}
	if !slices.Equal(got, want) {
		t.Errorf("SortReleases = %v, want %v", got, want)
	}

	newer, older := Nearest("1.21.4", ids)
	if newer != "1.21.5" || older != "1.21.1" {
		t.Errorf("Nearest(1.21.4) = %q, %q; want 1.21.5, 1.21.1", newer, older)
	}
	newer, older = Nearest("1.22", ids)
	if newer != "" || older != "1.21.10" {
		t.Errorf("Nearest(1.22) = %q, %q; want nothing newer, 1.21.10 older", newer, older)
	}
	newer, older = Nearest("1.19", ids)
	if newer != "1.20.6" || older != "" {
		t.Errorf("Nearest(1.19) = %q, %q; want 1.20.6 newer, nothing older", newer, older)
	}
}
