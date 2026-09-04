package mods

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"mcmgr/internal/config"
)

func TestRecommendedPerLoader(t *testing.T) {
	if got := Recommended(config.Vanilla); len(got) != 0 {
		t.Errorf("vanilla cannot load mods, but got %d recommendations", len(got))
	}

	for _, l := range []config.Loader{config.Fabric, config.Quilt, config.Paper, config.NeoForge} {
		all := Recommended(l)
		if len(all) == 0 {
			t.Errorf("%s: no recommendations", l)
		}
		defs := RecommendedDefaults(l)
		if len(defs) == 0 {
			t.Errorf("%s: nothing pre-selected", l)
		}
		if len(defs) >= len(all) {
			t.Errorf("%s: everything is pre-selected; the picker would have nothing to add", l)
		}
		seen := map[string]bool{}
		for _, r := range all {
			if seen[r.Slug] {
				t.Errorf("%s: %s is listed twice", l, r.Slug)
			}
			seen[r.Slug] = true
			if r.Why == "" || r.Name == "" {
				t.Errorf("%s: %s has no name or reason", l, r.Slug)
			}
		}
	}

	// Fabric API is the one thing a Fabric server nearly always needs, and it
	// does nothing on Paper.
	if !hasSlug(RecommendedDefaults(config.Fabric), "fabric-api") {
		t.Error("fabric: fabric-api should be pre-selected")
	}
	if hasSlug(Recommended(config.Paper), "fabric-api") {
		t.Error("paper: fabric-api must not be offered")
	}
	if hasSlug(Recommended(config.NeoForge), "fabric-api") {
		t.Error("neoforge: fabric-api must not be offered")
	}
}

func hasSlug(rs []Recommendation, slug string) bool {
	for _, r := range rs {
		if r.Slug == slug {
			return true
		}
	}
	return false
}

// TestRecommendationsExistOnModrinth checks every slug against the live API:
// that it exists, is strictly server-side (runs on the server, and does not
// need every player to install it), and is tagged for the loaders it is
// offered on. It runs only when MCMGR_TEST_NETWORK is set, so an offline test
// run is not blocked on it.
func TestRecommendationsExistOnModrinth(t *testing.T) {
	if os.Getenv("MCMGR_TEST_NETWORK") == "" {
		t.Skip("set MCMGR_TEST_NETWORK=1 to check recommendations against Modrinth")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	for _, r := range recommendations {
		resp, err := client.Get("https://api.modrinth.com/v2/project/" + r.Slug)
		if err != nil {
			t.Fatalf("%s: %v", r.Slug, err)
		}
		var proj struct {
			ServerSide string   `json:"server_side"`
			ClientSide string   `json:"client_side"`
			Loaders    []string `json:"loaders"`
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: HTTP %d", r.Slug, resp.StatusCode)
			resp.Body.Close()
			continue
		}
		err = json.NewDecoder(resp.Body).Decode(&proj)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s: %v", r.Slug, err)
		}
		p := Project{ServerSide: proj.ServerSide, ClientSide: proj.ClientSide}
		if !p.ServerSideOnly() {
			t.Errorf("%s is not strictly server-side (server_side=%s, client_side=%s) and must not be recommended",
				r.Slug, proj.ServerSide, proj.ClientSide)
		}
		for _, l := range config.AllLoaders {
			if !r.supports(l) {
				continue
			}
			ok := false
			for _, facet := range l.ModrinthFacets() {
				if containsFold(proj.Loaders, facet) {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s is offered for %s but Modrinth tags it only for %v", r.Slug, l, proj.Loaders)
			}
		}
	}
}
