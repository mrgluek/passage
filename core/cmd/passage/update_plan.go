package main

import (
	"archive/zip"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const maxWindowsArchiveSize = 512 << 20 // 512 MiB total uncompressed size limit

var knownWindowsFiles = map[string]bool{
	"passage.exe":      true,
	"passage-cli.exe":  true,
	"passage-iroh.exe": true,
	"LICENSE":          true,
}

// validateWindowsArchive checks that archive contains only known files
// under the expected top folder passage-<version>-windows-amd64/, without
// path traversal, backslashes, symlinks, or duplicate entries, and that
// passage.exe and passage-iroh.exe are present.
func validateWindowsArchive(archive, version string) ([]string, error) {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	defer z.Close()

	expectedTop := fmt.Sprintf("passage-%s-windows-amd64", version)
	var total uint64
	seen := make(map[string]bool)
	var files []string

	for _, f := range z.File {
		if strings.Contains(f.Name, "\\") {
			return nil, errors.New("invalid archive path: backslash not permitted")
		}
		clean := path.Clean(f.Name)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || (len(f.Name) >= 2 && f.Name[1] == ':') {
			return nil, errors.New("invalid archive path: path traversal or absolute")
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("invalid archive: symbolic link not permitted")
		}
		if f.UncompressedSize64 > maxWindowsArchiveSize-total {
			return nil, errors.New("update archive too large")
		}
		total += f.UncompressedSize64

		if clean == expectedTop {
			continue
		}
		if !strings.HasPrefix(clean, expectedTop+"/") {
			return nil, fmt.Errorf("unexpected entry outside %s: %s", expectedTop, clean)
		}
		rel := strings.TrimPrefix(clean, expectedTop+"/")
		if strings.Contains(rel, "/") {
			return nil, fmt.Errorf("unexpected directory in archive: %s", rel)
		}
		if !knownWindowsFiles[rel] {
			return nil, fmt.Errorf("unknown file in archive: %s", rel)
		}
		if seen[rel] {
			return nil, fmt.Errorf("duplicate entry in archive: %s", rel)
		}
		seen[rel] = true
		files = append(files, rel)
	}

	if !seen["passage.exe"] || !seen["passage-iroh.exe"] {
		return nil, errors.New("required executables missing from archive")
	}
	return files, nil
}

// verifyPEAmd64 verifies that the file at path is a valid PE amd64 binary.
func verifyPEAmd64(path string) error {
	f, err := pe.Open(path)
	if err != nil {
		return fmt.Errorf("invalid PE binary: %w", err)
	}
	defer f.Close()
	if f.FileHeader.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
		return fmt.Errorf("not an amd64 binary: machine 0x%x", f.FileHeader.Machine)
	}
	return nil
}

// extractAndVerifyWindowsUpdate extracts the validated known files from
// archive into stageDir and checks PE amd64 on all executables.
func extractAndVerifyWindowsUpdate(archive, version, stageDir string, files []string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()

	expectedTop := fmt.Sprintf("passage-%s-windows-amd64", version)
	fileSet := make(map[string]bool, len(files))
	for _, f := range files {
		fileSet[f] = true
	}

	for _, f := range z.File {
		clean := path.Clean(f.Name)
		if !strings.HasPrefix(clean, expectedTop+"/") {
			continue
		}
		rel := strings.TrimPrefix(clean, expectedTop+"/")
		if !fileSet[rel] || !knownWindowsFiles[rel] {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}
		destPath := filepath.Join(stageDir, rel)
		mode := os.FileMode(0600)
		if strings.HasSuffix(rel, ".exe") {
			mode = 0700
		}
		dst, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(dst, rc)
		rc.Close()
		dstErr := dst.Close()
		if err != nil {
			return err
		}
		if dstErr != nil {
			return dstErr
		}

		if strings.HasSuffix(rel, ".exe") {
			if err := verifyPEAmd64(destPath); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		}
	}

	return nil
}

type replacePlan struct {
	src         string
	dst         string
	backup      string
	hasExisting bool
}

func planReplacements(programDir, stageDir string, files []string) []replacePlan {
	var plans []replacePlan
	for _, file := range files {
		if !knownWindowsFiles[file] {
			continue
		}
		dst := filepath.Join(programDir, file)
		_, err := os.Stat(dst)
		plans = append(plans, replacePlan{
			src:         filepath.Join(stageDir, file),
			dst:         dst,
			backup:      filepath.Join(programDir, file+".old"),
			hasExisting: err == nil,
		})
	}
	return plans
}

func cleanOldBackups(plans []replacePlan) {
	for _, p := range plans {
		retryRemove(p.backup)
	}
}

// executeReplacements renames existing binaries to *.old and moves staged
// replacements into place. If any step fails, previous replacements are
// rolled back.
func executeReplacements(plans []replacePlan) (func() error, error) {
	var executed []replacePlan
	rollback := func() error {
		var lastErr error
		for i := len(executed) - 1; i >= 0; i-- {
			p := executed[i]
			retryRemove(p.dst)
			if p.hasExisting {
				if err := retryRename(p.backup, p.dst); err != nil {
					lastErr = err
				}
			}
		}
		return lastErr
	}

	for _, p := range plans {
		if p.hasExisting {
			retryRemove(p.backup)
			if err := retryRename(p.dst, p.backup); err != nil {
				rollback()
				return nil, fmt.Errorf("backup %s failed: %w", p.dst, err)
			}
		}
		if err := retryRename(p.src, p.dst); err != nil {
			if p.hasExisting {
				retryRename(p.backup, p.dst)
			}
			rollback()
			return nil, fmt.Errorf("replace %s failed: %w", p.dst, err)
		}
		executed = append(executed, p)
	}

	return rollback, nil
}

// rollbackUpdate restores *.old files to their original names in programDir.
func rollbackUpdate(programDir string) error {
	var lastErr error
	for file := range knownWindowsFiles {
		dst := filepath.Join(programDir, file)
		backup := filepath.Join(programDir, file+".old")
		if _, err := os.Stat(backup); err == nil {
			retryRemove(dst)
			if err := retryRename(backup, dst); err != nil {
				lastErr = err
			}
		}
	}
	return lastErr
}

// cleanLeftovers removes *.old files and .passage-update-* temp directories
// left behind by a previous update or crash.
func cleanLeftovers(programDir string) error {
	for file := range knownWindowsFiles {
		backup := filepath.Join(programDir, file+".old")
		retryRemove(backup)
	}
	entries, err := os.ReadDir(programDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), ".passage-update-") {
			os.RemoveAll(filepath.Join(programDir, e.Name()))
		}
	}
	return nil
}

func retryRename(src, dst string) error {
	timeout := 5 * time.Second
	deadline := time.Now().Add(timeout)
	interval := 50 * time.Millisecond
	for {
		err := os.Rename(src, dst)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("rename %s to %s failed after %v: %w", src, dst, timeout, err)
		}
		time.Sleep(interval)
		if interval < 250*time.Millisecond {
			interval *= 2
		}
	}
}

func retryRemove(path string) error {
	timeout := 5 * time.Second
	deadline := time.Now().Add(timeout)
	interval := 50 * time.Millisecond
	for {
		err := os.Remove(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("remove %s failed after %v: %w", path, timeout, err)
		}
		time.Sleep(interval)
		if interval < 250*time.Millisecond {
			interval *= 2
		}
	}
}

// awaitHealth reports whether a started update proved itself before exiting
// or running out of time.
func awaitHealth(healthy, exited <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-healthy:
		return true
	case <-exited:
		select {
		case <-healthy:
			return true
		default:
			return false
		}
	case <-timer.C:
		return false
	}
}
