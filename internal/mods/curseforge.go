package mods

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"mcmgr/internal/config"
	"mcmgr/internal/httpx"
)

const (
	curseForgeAPI = "https://api.curseforge.com/v1"
	// minecraftGameID is CurseForge's id for Minecraft.
	minecraftGameID = 432
	// classIDMod and classIDPlugin separate mods from Bukkit plugins.
	classIDMod    = 6
	classIDPlugin = 5
)

// CurseForge is the second provider. Unlike Modrinth its API requires a
// personal key, so it is only enabled when one is configured.
type CurseForge struct {
	key string
}

// NewCurseForge builds the provider, failing with actionable guidance when no
// key is available.
func NewCurseForge() (*CurseForge, error) {
	if k := curseForgeKey(); k != "" {
		return &CurseForge{key: k}, nil
	}
	return nil, fmt.Errorf("CurseForge needs an API key\n" +
		"       get one at https://console.curseforge.com/ then either:\n" +
		"         mcmgr config set global.curseforge_key <key>\n" +
		"         (or set MCMGR_CURSEFORGE_API_KEY in your environment)")
}

// curseForgeKey reads the key from the environment, falling back to the
// machine-wide settings file.
func curseForgeKey() string {
	for _, env := range []string{"MCMGR_CURSEFORGE_API_KEY", "CURSEFORGE_API_KEY"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	g, err := config.LoadGlobal()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(g.CurseForgeKey)
}

// Name implements Provider.
func (c *CurseForge) Name() string { return ProviderCurseForge }

func (c *CurseForge) headers() map[string]string {
	return map[string]string{"x-api-key": c.key}
}

type cfMod struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Summary       string `json:"summary"`
	DownloadCount int64  `json:"downloadCount"`
	Links         struct {
		WebsiteURL string `json:"websiteUrl"`
	} `json:"links"`
	Categories []struct {
		Name string `json:"name"`
	} `json:"categories"`
}

type cfFile struct {
	ID          int    `json:"id"`
	ModID       int    `json:"modId"`
	DisplayName string `json:"displayName"`
	FileName    string `json:"fileName"`
	ReleaseType int    `json:"releaseType"`
	FileDate    string `json:"fileDate"`
	DownloadURL string `json:"downloadUrl"`
	FileLength  int64  `json:"fileLength"`
	// GameVersions mixes Minecraft versions and loader names in one list.
	GameVersions []string `json:"gameVersions"`
	Hashes       []struct {
		Value string `json:"value"`
		Algo  int    `json:"algo"`
	} `json:"hashes"`
	Dependencies []struct {
		ModID        int `json:"modId"`
		RelationType int `json:"relationType"`
	} `json:"dependencies"`
}

// loaderTypeFor maps mcmgr loaders onto CurseForge's modLoaderType enum.
func loaderTypeFor(loaders []string) int {
	for _, l := range loaders {
		switch strings.ToLower(l) {
		case "forge":
			return 1
		case "fabric":
			return 4
		case "quilt":
			return 5
		case "neoforge":
			return 6
		}
	}
	return 0 // any
}

// classFor selects the mods or plugins catalogue.
func classFor(loaders []string) int {
	for _, l := range loaders {
		switch strings.ToLower(l) {
		case "paper", "spigot", "bukkit", "purpur", "folia":
			return classIDPlugin
		}
	}
	return classIDMod
}

// Search implements Provider.
func (c *CurseForge) Search(query string, f Filter, limit int) ([]Project, error) {
	if limit <= 0 {
		limit = 20
	}
	params := url.Values{}
	params.Set("gameId", strconv.Itoa(minecraftGameID))
	params.Set("searchFilter", query)
	params.Set("pageSize", strconv.Itoa(limit))
	params.Set("sortField", "2") // popularity
	params.Set("sortOrder", "desc")
	params.Set("classId", strconv.Itoa(classFor(f.Loaders)))
	if f.Minecraft != "" {
		params.Set("gameVersion", f.Minecraft)
	}
	if lt := loaderTypeFor(f.Loaders); lt != 0 {
		params.Set("modLoaderType", strconv.Itoa(lt))
	}

	var resp struct {
		Data []cfMod `json:"data"`
	}
	if err := httpx.GetJSON(curseForgeAPI+"/mods/search?"+params.Encode(), &resp, c.headers()); err != nil {
		return nil, fmt.Errorf("searching CurseForge: %w", err)
	}
	out := make([]Project, 0, len(resp.Data))
	for _, m := range resp.Data {
		out = append(out, c.project(m))
	}
	return out, nil
}

func (c *CurseForge) project(m cfMod) Project {
	cats := make([]string, 0, len(m.Categories))
	for _, cat := range m.Categories {
		cats = append(cats, cat.Name)
	}
	return Project{
		Provider:    ProviderCurseForge,
		ID:          strconv.Itoa(m.ID),
		Slug:        m.Slug,
		Name:        m.Name,
		Description: m.Summary,
		Downloads:   m.DownloadCount,
		Categories:  cats,
		URL:         m.Links.WebsiteURL,
	}
}

// Project implements Provider. CurseForge addresses mods by numeric id, so a
// slug is resolved through search first.
func (c *CurseForge) Project(idOrSlug string) (*Project, error) {
	if id, err := strconv.Atoi(idOrSlug); err == nil {
		var resp struct {
			Data cfMod `json:"data"`
		}
		u := fmt.Sprintf("%s/mods/%d", curseForgeAPI, id)
		if err := httpx.GetJSON(u, &resp, c.headers()); err != nil {
			if httpx.NotFound(err) {
				return nil, fmt.Errorf("no CurseForge mod with id %d", id)
			}
			return nil, err
		}
		p := c.project(resp.Data)
		return &p, nil
	}

	params := url.Values{}
	params.Set("gameId", strconv.Itoa(minecraftGameID))
	params.Set("slug", idOrSlug)
	var resp struct {
		Data []cfMod `json:"data"`
	}
	if err := httpx.GetJSON(curseForgeAPI+"/mods/search?"+params.Encode(), &resp, c.headers()); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("no CurseForge mod with slug %q", idOrSlug)
	}
	p := c.project(resp.Data[0])
	return &p, nil
}

// Versions implements Provider.
func (c *CurseForge) Versions(projectID string, f Filter) ([]Version, error) {
	id, err := strconv.Atoi(projectID)
	if err != nil {
		return nil, fmt.Errorf("CurseForge project ids are numeric, got %q", projectID)
	}

	params := url.Values{}
	params.Set("pageSize", "50")
	if f.Minecraft != "" {
		params.Set("gameVersion", f.Minecraft)
	}
	if lt := loaderTypeFor(f.Loaders); lt != 0 {
		params.Set("modLoaderType", strconv.Itoa(lt))
	}

	var resp struct {
		Data []cfFile `json:"data"`
	}
	u := fmt.Sprintf("%s/mods/%d/files?%s", curseForgeAPI, id, params.Encode())
	if err := httpx.GetJSON(u, &resp, c.headers()); err != nil {
		return nil, err
	}

	out := make([]Version, 0, len(resp.Data))
	for _, file := range resp.Data {
		v := c.convert(file)
		if v.matches(f) {
			out = append(out, v)
		}
	}
	return out, nil
}

// Version implements Provider. The file id alone is not addressable, so the
// mod id is encoded alongside it as "modID:fileID".
func (c *CurseForge) Version(versionID string) (*Version, error) {
	modID, fileID, ok := strings.Cut(versionID, ":")
	if !ok {
		return nil, fmt.Errorf("CurseForge version ids look like modID:fileID, got %q", versionID)
	}
	var resp struct {
		Data cfFile `json:"data"`
	}
	u := fmt.Sprintf("%s/mods/%s/files/%s", curseForgeAPI, url.PathEscape(modID), url.PathEscape(fileID))
	if err := httpx.GetJSON(u, &resp, c.headers()); err != nil {
		return nil, err
	}
	v := c.convert(resp.Data)
	return &v, nil
}

// releaseTypes maps CurseForge's numeric release types.
var releaseTypes = map[int]string{1: "release", 2: "beta", 3: "alpha"}

// knownLoaders are the names CurseForge mixes into gameVersions alongside
// actual Minecraft versions.
var knownLoaders = map[string]bool{
	"forge": true, "fabric": true, "quilt": true, "neoforge": true,
	"bukkit": true, "spigot": true, "paper": true, "purpur": true, "folia": true,
}

func (c *CurseForge) convert(f cfFile) Version {
	var sha1 string
	for _, h := range f.Hashes {
		if h.Algo == 1 { // 1 = SHA1, 2 = MD5
			sha1 = h.Value
			break
		}
	}

	// Split the mixed gameVersions list into Minecraft versions and loaders.
	var games, loaders []string
	for _, gv := range f.GameVersions {
		if knownLoaders[strings.ToLower(gv)] {
			loaders = append(loaders, strings.ToLower(gv))
		} else {
			games = append(games, gv)
		}
	}

	published, _ := time.Parse(time.RFC3339, f.FileDate)

	deps := make([]Dependency, 0, len(f.Dependencies))
	for _, d := range f.Dependencies {
		var kind string
		switch d.RelationType {
		case 3:
			kind = "required"
		case 2:
			kind = "optional"
		case 5:
			kind = "incompatible"
		case 1, 6:
			kind = "embedded"
		default:
			continue
		}
		deps = append(deps, Dependency{ProjectID: strconv.Itoa(d.ModID), Type: kind})
	}

	dl := f.DownloadURL
	if dl == "" {
		// Authors can opt out of third-party API downloads, which nulls the
		// URL. The CDN path is still derivable from the file id.
		dl = fmt.Sprintf("https://edge.forgecdn.net/files/%d/%d/%s", f.ID/1000, f.ID%1000, f.FileName)
	}

	return Version{
		Provider:     ProviderCurseForge,
		ID:           fmt.Sprintf("%d:%d", f.ModID, f.ID),
		ProjectID:    strconv.Itoa(f.ModID),
		Number:       f.DisplayName,
		Name:         f.DisplayName,
		Filename:     f.FileName,
		URL:          dl,
		SHA1:         sha1,
		Size:         f.FileLength,
		GameVersions: games,
		Loaders:      loaders,
		ReleaseType:  releaseTypes[f.ReleaseType],
		Published:    published,
		Dependencies: deps,
	}
}
