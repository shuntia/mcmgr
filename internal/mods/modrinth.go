package mods

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"mcmgr/internal/httpx"
)

// modrinthAPI is the v2 REST base. Modrinth asks for a descriptive User-Agent,
// which httpx sets globally.
const modrinthAPI = "https://api.modrinth.com/v2"

// Modrinth is the default provider: open API, no credentials required.
type Modrinth struct{}

// Name implements Provider.
func (m *Modrinth) Name() string { return ProviderModrinth }

type mrSearchResponse struct {
	Hits []struct {
		ProjectID   string   `json:"project_id"`
		Slug        string   `json:"slug"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Downloads   int64    `json:"downloads"`
		Categories  []string `json:"categories"`
		ClientSide  string   `json:"client_side"`
		ServerSide  string   `json:"server_side"`
	} `json:"hits"`
}

type mrProject struct {
	ID          string   `json:"id"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Downloads   int64    `json:"downloads"`
	Categories  []string `json:"categories"`
	ClientSide  string   `json:"client_side"`
	ServerSide  string   `json:"server_side"`
}

type mrVersion struct {
	ID            string   `json:"id"`
	ProjectID     string   `json:"project_id"`
	Name          string   `json:"name"`
	VersionNumber string   `json:"version_number"`
	VersionType   string   `json:"version_type"`
	GameVersions  []string `json:"game_versions"`
	Loaders       []string `json:"loaders"`
	DatePublished string   `json:"date_published"`
	Files         []struct {
		Hashes struct {
			SHA1   string `json:"sha1"`
			SHA512 string `json:"sha512"`
		} `json:"hashes"`
		URL      string `json:"url"`
		Filename string `json:"filename"`
		Primary  bool   `json:"primary"`
		Size     int64  `json:"size"`
	} `json:"files"`
	Dependencies []struct {
		VersionID      *string `json:"version_id"`
		ProjectID      *string `json:"project_id"`
		DependencyType string  `json:"dependency_type"`
	} `json:"dependencies"`
}

// facets builds Modrinth's facet filter: the outer array is ANDed, each inner
// array is ORed. Plugins and mods are both accepted so Paper servers can find
// Bukkit-family projects.
func facets(f Filter) string {
	groups := [][]string{{`"project_type:mod"`, `"project_type:plugin"`}}
	if f.Minecraft != "" {
		groups = append(groups, []string{fmt.Sprintf(`"versions:%s"`, f.Minecraft)})
	}
	if len(f.Loaders) > 0 {
		var g []string
		for _, l := range f.Loaders {
			g = append(g, fmt.Sprintf(`"categories:%s"`, l))
		}
		groups = append(groups, g)
	}
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = "[" + strings.Join(g, ",") + "]"
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// Search implements Provider.
func (m *Modrinth) Search(query string, f Filter, limit int) ([]Project, error) {
	if limit <= 0 {
		limit = 20
	}
	u := fmt.Sprintf("%s/search?query=%s&facets=%s&limit=%d&index=relevance",
		modrinthAPI, url.QueryEscape(query), url.QueryEscape(facets(f)), limit)

	var resp mrSearchResponse
	if err := httpx.GetJSON(u, &resp, nil); err != nil {
		return nil, fmt.Errorf("searching Modrinth: %w", err)
	}
	out := make([]Project, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		out = append(out, Project{
			Provider:    ProviderModrinth,
			ID:          h.ProjectID,
			Slug:        h.Slug,
			Name:        h.Title,
			Description: h.Description,
			Downloads:   h.Downloads,
			Categories:  h.Categories,
			URL:         "https://modrinth.com/mod/" + h.Slug,
			ClientSide:  h.ClientSide,
			ServerSide:  h.ServerSide,
		})
	}
	return out, nil
}

// Project implements Provider.
func (m *Modrinth) Project(idOrSlug string) (*Project, error) {
	var p mrProject
	u := fmt.Sprintf("%s/project/%s", modrinthAPI, url.PathEscape(idOrSlug))
	if err := httpx.GetJSON(u, &p, nil); err != nil {
		if httpx.NotFound(err) {
			return nil, fmt.Errorf("no Modrinth project named %q", idOrSlug)
		}
		return nil, err
	}
	return &Project{
		Provider:    ProviderModrinth,
		ID:          p.ID,
		Slug:        p.Slug,
		Name:        p.Title,
		Description: p.Description,
		Downloads:   p.Downloads,
		Categories:  p.Categories,
		URL:         "https://modrinth.com/mod/" + p.Slug,
		ClientSide:  p.ClientSide,
		ServerSide:  p.ServerSide,
	}, nil
}

// Versions implements Provider. The loader and game-version filters are passed
// upstream where possible and re-checked locally, since Modrinth ignores
// unknown loader names rather than erroring.
func (m *Modrinth) Versions(projectID string, f Filter) ([]Version, error) {
	q := fmt.Sprintf("%s/project/%s/version", modrinthAPI, url.PathEscape(projectID))
	var params []string
	if len(f.Loaders) > 0 {
		params = append(params, "loaders="+url.QueryEscape(jsonList(f.Loaders)))
	}
	if f.Minecraft != "" {
		params = append(params, "game_versions="+url.QueryEscape(jsonList([]string{f.Minecraft})))
	}
	if len(params) > 0 {
		q += "?" + strings.Join(params, "&")
	}

	var raw []mrVersion
	if err := httpx.GetJSON(q, &raw, nil); err != nil {
		if httpx.NotFound(err) {
			return nil, fmt.Errorf("no Modrinth project with id %q", projectID)
		}
		return nil, err
	}

	out := make([]Version, 0, len(raw))
	for _, v := range raw {
		conv, err := m.convert(v)
		if err != nil {
			continue // a version with no downloadable primary file is unusable
		}
		if conv.matches(f) {
			out = append(out, *conv)
		}
	}
	return out, nil
}

// Version implements Provider.
func (m *Modrinth) Version(versionID string) (*Version, error) {
	var v mrVersion
	u := fmt.Sprintf("%s/version/%s", modrinthAPI, url.PathEscape(versionID))
	if err := httpx.GetJSON(u, &v, nil); err != nil {
		if httpx.NotFound(err) {
			return nil, fmt.Errorf("no Modrinth version with id %q", versionID)
		}
		return nil, err
	}
	return m.convert(v)
}

// convert maps a Modrinth version onto the shared type, choosing the primary
// file (the one the project marks as its main artifact).
func (m *Modrinth) convert(v mrVersion) (*Version, error) {
	if len(v.Files) == 0 {
		return nil, fmt.Errorf("version %s has no files", v.ID)
	}
	file := v.Files[0]
	for _, f := range v.Files {
		if f.Primary {
			file = f
			break
		}
	}

	published, _ := time.Parse(time.RFC3339, v.DatePublished)

	deps := make([]Dependency, 0, len(v.Dependencies))
	for _, d := range v.Dependencies {
		dep := Dependency{Type: d.DependencyType}
		if d.ProjectID != nil {
			dep.ProjectID = *d.ProjectID
		}
		if d.VersionID != nil {
			dep.VersionID = *d.VersionID
		}
		if dep.ProjectID == "" && dep.VersionID == "" {
			continue
		}
		deps = append(deps, dep)
	}

	return &Version{
		Provider:     ProviderModrinth,
		ID:           v.ID,
		ProjectID:    v.ProjectID,
		Number:       v.VersionNumber,
		Name:         v.Name,
		Filename:     file.Filename,
		URL:          file.URL,
		SHA1:         file.Hashes.SHA1,
		Size:         file.Size,
		GameVersions: v.GameVersions,
		Loaders:      v.Loaders,
		ReleaseType:  v.VersionType,
		Published:    published,
		Dependencies: deps,
	}, nil
}

// VersionsByHash identifies files by their sha1, which is how mcmgr recognises
// add-ons that were installed before it was involved. It returns a map keyed
// by the hashes that matched; unknown files are simply absent.
func (m *Modrinth) VersionsByHash(sha1s []string) (map[string]Version, error) {
	if len(sha1s) == 0 {
		return map[string]Version{}, nil
	}
	body := struct {
		Hashes    []string `json:"hashes"`
		Algorithm string   `json:"algorithm"`
	}{Hashes: sha1s, Algorithm: "sha1"}

	var raw map[string]mrVersion
	if err := httpx.PostJSON(modrinthAPI+"/version_files", body, &raw, nil); err != nil {
		return nil, fmt.Errorf("looking up files on Modrinth: %w", err)
	}

	out := make(map[string]Version, len(raw))
	for hash, v := range raw {
		conv, err := m.convert(v)
		if err != nil {
			continue
		}
		out[strings.ToLower(hash)] = *conv
	}
	return out, nil
}

// Projects fetches several projects at once, to avoid a request per mod when
// adopting a directory full of them.
func (m *Modrinth) Projects(ids []string) (map[string]Project, error) {
	out := map[string]Project{}
	if len(ids) == 0 {
		return out, nil
	}
	u := fmt.Sprintf("%s/projects?ids=%s", modrinthAPI, url.QueryEscape(jsonList(ids)))

	var raw []mrProject
	if err := httpx.GetJSON(u, &raw, nil); err != nil {
		return nil, err
	}
	for _, p := range raw {
		out[p.ID] = Project{
			Provider:    ProviderModrinth,
			ID:          p.ID,
			Slug:        p.Slug,
			Name:        p.Title,
			Description: p.Description,
			Downloads:   p.Downloads,
			Categories:  p.Categories,
			URL:         "https://modrinth.com/mod/" + p.Slug,
			ClientSide:  p.ClientSide,
			ServerSide:  p.ServerSide,
		}
	}
	return out, nil
}

// jsonList renders a string slice as a JSON array, which is how Modrinth
// expects list query parameters.
func jsonList(items []string) string {
	b, _ := json.Marshal(items)
	return string(b)
}
