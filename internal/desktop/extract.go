package desktop

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// extractBundle unpacks a .app.tar.gz into dest, stripping the bundle's own
// top-level directory. The tarball contains `Sonar.app/Contents/...`, and dest
// is the path the bundle will end up at, so `Sonar.app/` has to come off:
// unpacking it verbatim would nest a Sonar.app inside the temporary directory
// and the atomic rename would then move a directory *containing* a bundle into
// /Applications.
//
// A bundle is not a flat file list. Its Frameworks and Resources are reached
// through symlinks, and its executables are only executable because of their
// mode bits, so both are carried across; nothing is chmod'd afterwards and
// nothing is flattened.
func extractBundle(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s is not a gzip archive: %w", filepath.Base(archive), err)
	}
	defer gz.Close()

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	tr := tar.NewReader(gz)
	var entries int
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", filepath.Base(archive), err)
		}

		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." || name == "/" || isAppleDouble(name) {
			continue
		}
		rel := stripBundlePrefix(name)
		if rel == "" {
			continue
		}

		target, err := safeJoin(dest, rel)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, hdr.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			entries++
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// A symlink inside a bundle points within the bundle
			// (Versions/Current, Resources -> A/Resources). One that escapes
			// dest is either a broken build or an attack, and neither is
			// something to unpack.
			if _, err := safeJoin(dest, path.Join(path.Dir(rel), hdr.Linkname)); err != nil {
				if !path.IsAbs(hdr.Linkname) {
					return err
				}
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
			entries++
		case tar.TypeLink:
			source, err := safeJoin(dest, stripBundlePrefix(path.Clean(hdr.Linkname)))
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Link(source, target); err != nil {
				return err
			}
			entries++
		default:
			// Character devices, fifos and the rest have no business in an
			// application bundle; skipping them is safer than creating them.
		}
	}

	if entries == 0 {
		return fmt.Errorf("%s contains no application bundle", filepath.Base(archive))
	}
	return nil
}

// stripBundlePrefix removes the archive's own `Something.app/` directory from
// an entry, when it has one. It is decided per entry rather than once from the
// first, because the first entry is not reliably the bundle: `tar` on macOS
// emits AppleDouble entries ahead of the file they belong to, and reading the
// prefix off one of those put every real file one directory too deep.
func stripBundlePrefix(name string) string {
	head, rest, _ := strings.Cut(name, "/")
	if strings.HasSuffix(head, ".app") {
		// rest is empty for the bundle's own directory entry, and the caller
		// skips it: dest *is* that directory, so unpacking it would nest a
		// second Sonar.app inside the installed one.
		return rest
	}
	return name
}

// isAppleDouble reports whether an entry is the ._name sidecar `tar` on macOS
// writes to carry a file's extended attributes. The bundle does not need them,
// and unpacking them would leave ._Sonar.app and friends inside the installed
// app.
func isAppleDouble(name string) bool {
	return strings.HasPrefix(path.Base(name), "._")
}

// safeJoin refuses any entry that would land outside root, whatever the
// archive claims its path is.
func safeJoin(root, rel string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(rel))
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry %q escapes the install directory", rel)
	}
	return target, nil
}
