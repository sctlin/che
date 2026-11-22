// Package install places downloaded artifacts into a staging dir. Every write
// goes through os.Root, so archive entries can't escape the dir.
package install

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/utils"
	"github.com/ulikunitz/xz"
)

const dirMode = 0o755

// Unpack places file into dir, an existing empty staging dir. Archives are
// extracted as is. Single-file formats are written to dir/name with mode 0755.
func Unpack(format config.Format, file, dir, name string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()

	if format == config.Zip {
		return extractZip(file, root)
	}

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	switch format {
	case config.TarGz:
		zr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		return extractTar(zr, root)
	case config.TarXz:
		xr, err := xz.NewReader(f)
		if err != nil {
			return err
		}
		return extractTar(xr, root)
	case config.Gz:
		zr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		return writeFile(root, name, zr, 0o755)
	case config.Xz:
		xr, err := xz.NewReader(f)
		if err != nil {
			return err
		}
		return writeFile(root, name, xr, 0o755)
	case config.Binary:
		return writeFile(root, name, f, 0o755)
	}
	return fmt.Errorf("unsupported format %q", format)
}

func extractTar(r io.Reader, root *os.Root) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		switch h.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(h.Name, dirMode)
		case tar.TypeReg:
			err = writeFile(root, h.Name, tr, h.FileInfo().Mode().Perm())
		case tar.TypeSymlink:
			err = symlink(root, h.Linkname, h.Name)
		case tar.TypeLink:
			if err = mkParent(root, h.Name); err == nil {
				err = root.Link(h.Linkname, h.Name)
			}
		case tar.TypeXGlobalHeader:
			continue
		default:
			err = fmt.Errorf("unsupported entry type %q", h.Typeflag)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", h.Name, err)
		}
	}
}

// Zips made on Windows carry no exec bit, the executor sets it on bins.
func extractZip(file string, root *os.Root) error {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		if err := extractZipEntry(f, root); err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return nil
}

func extractZipEntry(f *zip.File, root *os.Root) error {
	mode := f.Mode()
	if mode.IsDir() {
		return root.MkdirAll(f.Name, dirMode)
	}

	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	switch {
	case mode&fs.ModeSymlink != 0:
		target, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		return symlink(root, string(target), f.Name)
	case mode.IsRegular():
		return writeFile(root, f.Name, rc, mode.Perm())
	}
	return fmt.Errorf("unsupported entry mode %s", mode)
}

// symlink creates name pointing to target. Targets outside the root are
// created, but root refuses to follow them, so nothing is written through them.
func symlink(root *os.Root, target, name string) error {
	if err := mkParent(root, name); err != nil {
		return err
	}
	return root.Symlink(target, name)
}

func writeFile(root *os.Root, name string, r io.Reader, perm fs.FileMode) (err error) {
	if err := mkParent(root, name); err != nil {
		return err
	}

	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()

	_, err = io.Copy(f, r)
	return err
}

func mkParent(root *os.Root, name string) error {
	return root.MkdirAll(filepath.Dir(name), dirMode)
}

// ResolveBin expands vars in pattern, globs it inside dir and returns the path,
// relative to dir, of the single regular file it matches. Relative, since the
// staging dir is renamed into place after.
func ResolveBin(dir, pattern string, vars map[string]string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()

	pattern = utils.ExpandVars(pattern, vars)
	matches, err := fs.Glob(root.FS(), pattern)
	if err != nil {
		return "", fmt.Errorf("bin %s: %w", pattern, err)
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("bin %s: no match", pattern)
	case 1:
	default:
		return "", fmt.Errorf("bin %s: several matches: %s", pattern, strings.Join(matches, ", "))
	}

	// Stat follows symlinks only inside the root, one pointing out is an error.
	info, err := root.Stat(matches[0])
	if err != nil {
		return "", fmt.Errorf("bin %s: %w", pattern, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("bin %s: %s is not a regular file", pattern, matches[0])
	}
	return matches[0], nil
}
