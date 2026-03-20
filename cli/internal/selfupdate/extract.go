package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// extractBinary extracts the "sonar" binary from a .tar.gz and returns the path to the extracted file.
func extractBinary(tarPath string) (string, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("invalid archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading archive: %w", err)
		}

		if filepath.Base(hdr.Name) == "sonar" && hdr.Typeflag == tar.TypeReg {
			tmpBin, err := os.CreateTemp("", "sonar-bin-*")
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(tmpBin, tr); err != nil {
				tmpBin.Close()
				os.Remove(tmpBin.Name())
				return "", err
			}
			tmpBin.Close()
			os.Chmod(tmpBin.Name(), 0755)
			return tmpBin.Name(), nil
		}
	}

	return "", fmt.Errorf("sonar binary not found in archive")
}

// replaceBinary atomically replaces oldPath with newPath.
func replaceBinary(oldPath, newPath string) error {
	// Rename is atomic on the same filesystem.
	// Since temp may be on a different fs, copy + rename instead.
	tmpDest := oldPath + ".new"
	src, err := os.Open(newPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(tmpDest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", tmpDest, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmpDest)
		return err
	}
	dst.Close()

	if err := os.Rename(tmpDest, oldPath); err != nil {
		os.Remove(tmpDest)
		return fmt.Errorf("cannot replace binary: %w", err)
	}
	return nil
}
