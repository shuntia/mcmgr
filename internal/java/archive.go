package java

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxEntrySize bounds a single unpacked file, so a malformed or hostile
// archive cannot fill the disk.
const maxEntrySize = 1 << 30 // 1 GiB

// extract unpacks a .zip or .tar.gz into dst.
func extract(archive, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(strings.ToLower(archive), ".zip"):
		return extractZip(archive, dst)
	case strings.HasSuffix(strings.ToLower(archive), ".tar.gz"):
		return extractTarGz(archive, dst)
	}
	return fmt.Errorf("unsupported archive type: %s", filepath.Base(archive))
}

// safePath joins an archive entry onto the destination, refusing any name that
// would write outside it.
//
// Archive entries are attacker-controlled data: a name like "../../evil" would
// otherwise escape the directory being unpacked into. This is the zip-slip
// check, and it applies to tar just as much as zip.
func safePath(dst, name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || cleaned == ".." {
		return "", fmt.Errorf("archive entry %q would write outside the target directory", name)
	}
	full := filepath.Join(dst, cleaned)

	// Belt and braces: confirm the result really is under dst.
	rel, err := filepath.Rel(dst, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q would write outside the target directory", name)
	}
	return full, nil
}

func extractZip(archive, dst string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		target, err := safePath(dst, f.Name)
		if err != nil {
			return err
		}
		info := f.FileInfo()
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(target, rc, info.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safePath(dst, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// The mode matters here: bin/java has to keep its execute bit.
			if err := writeFile(target, tr, hdr.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Temurin uses symlinks inside the runtime, e.g. for man pages.
			// Refuse any that point outside the tree.
			if _, err := safePath(dst, filepath.Join(filepath.Dir(hdr.Name), hdr.Linkname)); err != nil {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				// A platform that cannot make the link is not a fatal problem
				// for running java.
				continue
			}
		}
	}
}

// writeFile copies one entry to disk with the given mode.
func writeFile(target string, r io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm()|0o200)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, io.LimitReader(r, maxEntrySize)); err != nil {
		return err
	}
	return out.Chmod(mode.Perm())
}
