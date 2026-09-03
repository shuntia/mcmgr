package httpx

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestBuildVersionFormatting(t *testing.T) {
	settings := func(revision string, modified bool) []debug.BuildSetting {
		out := []debug.BuildSetting{{Key: "GOOS", Value: "linux"}}
		if revision != "" {
			out = append(out, debug.BuildSetting{Key: "vcs.revision", Value: revision})
		}
		if modified {
			out = append(out, debug.BuildSetting{Key: "vcs.modified", Value: "true"})
		}
		return out
	}

	cases := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{
			name: "no build info at all",
			want: fallbackVersion,
		},
		{
			name: "a plain go build with no VCS data",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			ok:   true,
			want: fallbackVersion,
		},
		{
			name: "a tagged release",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}},
			ok:   true,
			want: "1.2.3",
		},
		{
			name: "a build from a clean checkout",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: settings("9f3c1abcdef0123456789", false),
			},
			ok:   true,
			want: fallbackVersion + "+9f3c1ab",
		},
		{
			name: "a build with uncommitted changes",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: settings("9f3c1abcdef0123456789", true),
			},
			ok:   true,
			want: fallbackVersion + "+9f3c1ab+dirty",
		},
		{
			// The toolchain synthesises this for a module with no tag, and
			// may already have stamped +dirty onto it.
			name: "a pseudo-version carrying its own metadata",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "v0.0.0-20260903070259-b6599612fddf+dirty"},
				Settings: settings("b659961aaaa", true),
			},
			ok:   true,
			want: fallbackVersion + "+b659961+dirty",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildVersion(c.info, c.ok); got != c.want {
				t.Errorf("buildVersion = %q, want %q", got, c.want)
			}
		})
	}
}

// TestUserAgentCarriesTheVersion is what Modrinth actually sees; a generic
// agent gets rate-limited harder, and one with no version is useless in a bug
// report.
func TestUserAgentCarriesTheVersion(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must never be empty")
	}
	if !strings.HasPrefix(UserAgent, "mcmgr/"+Version+" ") {
		t.Errorf("UserAgent = %q, want it to start with mcmgr/%s", UserAgent, Version)
	}
	if !strings.Contains(UserAgent, "github.com/shuntia/mcmgr") {
		t.Errorf("UserAgent = %q, want it to name the project", UserAgent)
	}
}
