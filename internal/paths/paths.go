// Package paths resolves the per-user directories mcmgr stores state in,
// following each platform's own convention via os.UserConfigDir/UserCacheDir.
package paths

import (
	"os"
	"path/filepath"
)

// ConfigDir is where the server registry and global settings live:
//
//	Windows  %AppData%\mcmgr
//	macOS    ~/Library/Application Support/mcmgr
//	Linux    ${XDG_CONFIG_HOME:-~/.config}/mcmgr
func ConfigDir() (string, error) {
	if v := os.Getenv("MCMGR_HOME"); v != "" {
		return v, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mcmgr"), nil
}

// CacheDir is where downloaded jars and API responses are cached.
func CacheDir() (string, error) {
	if v := os.Getenv("MCMGR_CACHE"); v != "" {
		return v, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mcmgr"), nil
}

// EnsureConfigDir creates and returns the config directory.
func EnsureConfigDir() (string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return d, os.MkdirAll(d, 0o755)
}

// EnsureCacheDir creates and returns the cache directory.
func EnsureCacheDir() (string, error) {
	d, err := CacheDir()
	if err != nil {
		return "", err
	}
	return d, os.MkdirAll(d, 0o755)
}
