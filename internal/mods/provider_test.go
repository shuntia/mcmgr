package mods

import (
	"errors"
	"testing"
	"time"

	"mcmgr/internal/config"
)

func TestChannelsAccepts(t *testing.T) {
	none := Channels{}
	beta := Channels{Beta: true}
	both := Channels{Beta: true, Alpha: true}

	// A full release is always eligible, however the channels are set.
	for _, ch := range []Channels{none, beta, both} {
		for _, rt := range []string{"", "release"} {
			if !ch.Accepts(rt) {
				t.Errorf("%+v should accept release type %q", ch, rt)
			}
		}
	}

	if none.Accepts("beta") || none.Accepts("alpha") {
		t.Error("with no channels enabled, pre-releases must be refused")
	}
	if !beta.Accepts("beta") {
		t.Error("allow_beta should accept beta")
	}
	if beta.Accepts("alpha") {
		t.Error("allow_beta alone must not accept alpha: the two are independent")
	}
	if !both.Accepts("alpha") || !both.Accepts("beta") {
		t.Error("both channels enabled should accept both")
	}

	// Alpha without beta is unusual but must be honoured, since the settings
	// are independent rather than a ladder.
	alphaOnly := Channels{Alpha: true}
	if alphaOnly.Accepts("beta") {
		t.Error("allow_alpha must not imply allow_beta")
	}
	if !alphaOnly.Accepts("alpha") {
		t.Error("allow_alpha should accept alpha")
	}

	// An unrecognised type is treated as a release rather than hidden.
	if !none.Accepts("something-else") {
		t.Error("an unknown release type should not be filtered out")
	}
}

func TestChannelsFromDefaults(t *testing.T) {
	ch := ChannelsFrom(config.Defaults{AllowBeta: true})
	if !ch.Beta || ch.Alpha {
		t.Errorf("ChannelsFrom = %+v, want beta only", ch)
	}
	if !ch.Any() {
		t.Error("Any() should be true when a channel is enabled")
	}
	if (Channels{}).Any() {
		t.Error("Any() should be false with nothing enabled")
	}
	if got := (Channels{Beta: true, Alpha: true}).Names(); len(got) != 2 {
		t.Errorf("Names() = %v, want both channels", got)
	}
}

// version is a small helper for building candidates with distinct dates.
func version(number, releaseType string, daysAgo int) Version {
	return Version{
		Number:      number,
		ReleaseType: releaseType,
		Published:   time.Now().AddDate(0, 0, -daysAgo),
	}
}

func TestPickBestHonoursChannels(t *testing.T) {
	// A newer beta alongside an older full release.
	candidates := []Version{
		version("1.1-beta", "beta", 1),
		version("1.0", "release", 10),
		version("1.2-alpha", "alpha", 0),
	}

	// With nothing enabled, only the release is eligible.
	got, err := pickBest(candidates, Filter{})
	if err != nil {
		t.Fatalf("pickBest: %v", err)
	}
	if got.Number != "1.0" {
		t.Errorf("with no channels enabled, picked %q, want the full release 1.0", got.Number)
	}

	// Opting into beta means wanting betas, so the newer beta wins over the
	// older release.
	got, err = pickBest(candidates, Filter{Channels: Channels{Beta: true}})
	if err != nil {
		t.Fatalf("pickBest: %v", err)
	}
	if got.Number != "1.1-beta" {
		t.Errorf("with beta enabled, picked %q, want 1.1-beta", got.Number)
	}

	// Alpha enabled as well brings the newest of all into play.
	got, err = pickBest(candidates, Filter{Channels: Channels{Beta: true, Alpha: true}})
	if err != nil {
		t.Fatalf("pickBest: %v", err)
	}
	if got.Number != "1.2-alpha" {
		t.Errorf("with both channels, picked %q, want 1.2-alpha", got.Number)
	}
}

// TestPickBestReportsBlockedChannels covers the message that tells the user
// which setting would unlock the build they cannot have.
func TestPickBestReportsBlockedChannels(t *testing.T) {
	betaOnly := []Version{version("1.1-beta", "beta", 1), version("1.0-beta", "beta", 5)}

	_, err := pickBest(betaOnly, Filter{})
	if err == nil {
		t.Fatal("beta-only candidates with beta disabled should fail")
	}
	if !errors.Is(err, errUnstableOnly) {
		t.Errorf("should identify as the pre-release case, got %v", err)
	}

	var detail *UnstableOnlyError
	if !errors.As(err, &detail) {
		t.Fatalf("want an *UnstableOnlyError, got %T", err)
	}
	if len(detail.Channels) != 1 || detail.Channels[0] != "beta" {
		t.Errorf("Channels = %v, want [beta]", detail.Channels)
	}
	if detail.Enable() != "--allow-beta" {
		t.Errorf("Enable() = %q, want --allow-beta", detail.Enable())
	}
	if detail.Setting() != "defaults.allow_beta" {
		t.Errorf("Setting() = %q, want defaults.allow_beta", detail.Setting())
	}

	// Both channels blocked should name both.
	mixed := []Version{version("2-alpha", "alpha", 1), version("1-beta", "beta", 2)}
	_, err = pickBest(mixed, Filter{})
	if !errors.As(err, &detail) {
		t.Fatalf("want an *UnstableOnlyError, got %T", err)
	}
	if len(detail.Channels) != 2 {
		t.Errorf("Channels = %v, want both beta and alpha", detail.Channels)
	}

	// Enabling one of two still leaves the other usable.
	got, perr := pickBest(mixed, Filter{Channels: Channels{Beta: true}})
	if perr != nil {
		t.Fatalf("pickBest: %v", perr)
	}
	if got.Number != "1-beta" {
		t.Errorf("picked %q, want the beta since alpha is still off", got.Number)
	}
}

// TestPickBestNoCandidates keeps "nothing exists" distinct from "nothing
// enabled", since the two call for different responses.
func TestPickBestNoCandidates(t *testing.T) {
	_, err := pickBest(nil, Filter{})
	if !errors.Is(err, errNoCompatible) {
		t.Errorf("an empty candidate list should report errNoCompatible, got %v", err)
	}
	if errors.Is(err, errUnstableOnly) {
		t.Error("no candidates at all is not the pre-release case")
	}
}
