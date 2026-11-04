package main

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func minimalPE(machine uint16) []byte {
	dos := make([]byte, 128)
	dos[0] = 'M'
	dos[1] = 'Z'
	dos[0x3c] = 0x80
	peSig := []byte{'P', 'E', 0, 0}
	coff := make([]byte, 20)
	coff[0] = byte(machine)
	coff[1] = byte(machine >> 8)
	coff[18] = 0x02 // IMAGE_FILE_EXECUTABLE_IMAGE
	data := append(dos, peSig...)
	return append(data, coff...)
}

func createTestZip(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "update.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for name, content := range entries {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if content == nil {
			// directory or symlink indicator if needed
			h.Name += "/"
			if _, err := w.CreateHeader(h); err != nil {
				t.Fatal(err)
			}
			continue
		}
		pw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return zipPath
}

func TestValidateWindowsArchiveGood(t *testing.T) {
	version := "1.2.3"
	top := fmt.Sprintf("passage-%s-windows-amd64", version)
	archive := createTestZip(t, map[string][]byte{
		top:                       nil,
		top + "/passage.exe":      minimalPE(0x8664),
		top + "/passage-iroh.exe": minimalPE(0x8664),
		top + "/passage-cli.exe":  minimalPE(0x8664),
		top + "/LICENSE":          []byte("license terms"),
	})

	files, err := validateWindowsArchive(archive, version)
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if len(files) != 4 {
		t.Fatalf("expected 4 files, got %d: %v", len(files), files)
	}
}

func TestValidateWindowsArchiveRejections(t *testing.T) {
	version := "1.0.0"
	top := fmt.Sprintf("passage-%s-windows-amd64", version)
	pe := minimalPE(0x8664)

	tests := []struct {
		name    string
		entries map[string][]byte
		symlink string
	}{
		{
			name: "backslash trick",
			entries: map[string][]byte{
				top + "\\passage.exe":     pe,
				top + "/passage-iroh.exe": pe,
			},
		},
		{
			name: "relative traversal dot dot",
			entries: map[string][]byte{
				top + "/../escape.exe":    pe,
				top + "/passage.exe":      pe,
				top + "/passage-iroh.exe": pe,
			},
		},
		{
			name: "absolute path leading slash",
			entries: map[string][]byte{
				"/passage.exe":            pe,
				top + "/passage.exe":      pe,
				top + "/passage-iroh.exe": pe,
			},
		},
		{
			name: "wrong top folder",
			entries: map[string][]byte{
				"passage-9.9.9-windows-amd64/passage.exe":      pe,
				"passage-9.9.9-windows-amd64/passage-iroh.exe": pe,
			},
		},
		{
			name: "unknown file",
			entries: map[string][]byte{
				top + "/passage.exe":      pe,
				top + "/passage-iroh.exe": pe,
				top + "/malicious.dll":    []byte("evil"),
			},
		},
		{
			name: "nested subdirectory",
			entries: map[string][]byte{
				top + "/sub/passage.exe":  pe,
				top + "/passage.exe":      pe,
				top + "/passage-iroh.exe": pe,
			},
		},
		{
			name: "missing passage.exe",
			entries: map[string][]byte{
				top + "/passage-iroh.exe": pe,
				top + "/LICENSE":          []byte("lic"),
			},
		},
		{
			name: "missing passage-iroh.exe",
			entries: map[string][]byte{
				top + "/passage.exe": pe,
				top + "/LICENSE":     []byte("lic"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			zipPath := createTestZip(t, tc.entries)
			_, err := validateWindowsArchive(zipPath, version)
			if err == nil {
				t.Fatalf("%s: expected validation to fail, but it succeeded", tc.name)
			}
		})
	}
}

func TestValidateWindowsArchiveSymlink(t *testing.T) {
	version := "1.0.0"
	top := fmt.Sprintf("passage-%s-windows-amd64", version)
	zipPath := filepath.Join(t.TempDir(), "symlink.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	h := &zip.FileHeader{Name: top + "/passage.exe"}
	h.SetMode(os.ModeSymlink | 0777)
	pw, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	pw.Write([]byte("target"))
	w.Close()

	if _, err := validateWindowsArchive(zipPath, version); err == nil {
		t.Fatal("expected symlink archive to be rejected")
	}
}

func TestValidateWindowsArchiveOversized(t *testing.T) {
	version := "1.0.0"
	top := fmt.Sprintf("passage-%s-windows-amd64", version)
	zipPath := filepath.Join(t.TempDir(), "oversized.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	h := &zip.FileHeader{Name: top + "/passage.exe", UncompressedSize64: maxWindowsArchiveSize + 1}
	pw, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	pw.Write([]byte("small"))
	w.Close()

	if _, err := validateWindowsArchive(zipPath, version); err == nil {
		t.Fatal("expected oversized archive to be rejected")
	}
}

func TestPEAmd64Verification(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "good.exe")
	if err := os.WriteFile(good, minimalPE(0x8664), 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyPEAmd64(good); err != nil {
		t.Fatalf("expected valid PE amd64, got error: %v", err)
	}

	notPE := filepath.Join(dir, "notpe.exe")
	if err := os.WriteFile(notPE, []byte("this is not pe"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyPEAmd64(notPE); err == nil {
		t.Fatal("expected non-PE file to be rejected")
	}

	wrongArch := filepath.Join(dir, "i386.exe")
	if err := os.WriteFile(wrongArch, minimalPE(0x14c), 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyPEAmd64(wrongArch); err == nil {
		t.Fatal("expected wrong architecture to be rejected")
	}
}

func TestExtractAndVerifyWindowsUpdate(t *testing.T) {
	version := "1.0.0"
	top := fmt.Sprintf("passage-%s-windows-amd64", version)
	pe := minimalPE(0x8664)

	archive := createTestZip(t, map[string][]byte{
		top + "/passage.exe":      pe,
		top + "/passage-iroh.exe": pe,
		top + "/passage-cli.exe":  pe,
		top + "/LICENSE":          []byte("license content"),
	})

	files, err := validateWindowsArchive(archive, version)
	if err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(t.TempDir(), "stage")
	if err := os.MkdirAll(stageDir, 0700); err != nil {
		t.Fatal(err)
	}

	if err := extractAndVerifyWindowsUpdate(archive, version, stageDir, files); err != nil {
		t.Fatalf("unexpected extract error: %v", err)
	}

	for _, name := range []string{"passage.exe", "passage-iroh.exe", "passage-cli.exe", "LICENSE"} {
		dest := filepath.Join(stageDir, name)
		if _, err := os.Stat(dest); err != nil {
			t.Fatalf("expected extracted file %s to exist: %v", name, err)
		}
	}

	// Corrupt an exe and ensure verification fails
	corruptArchive := createTestZip(t, map[string][]byte{
		top + "/passage.exe":      []byte("corrupt"),
		top + "/passage-iroh.exe": pe,
	})
	files, _ = validateWindowsArchive(corruptArchive, version)
	corruptStage := filepath.Join(t.TempDir(), "corrupt-stage")
	os.MkdirAll(corruptStage, 0700)
	if err := extractAndVerifyWindowsUpdate(corruptArchive, version, corruptStage, files); err == nil {
		t.Fatal("expected corrupt executable to fail PE verification during extraction")
	}
}

func TestExecuteReplacementsAndRollback(t *testing.T) {
	programDir := t.TempDir()
	stageDir := t.TempDir()

	// Initial files in programDir
	for _, name := range []string{"passage.exe", "passage-iroh.exe", "LICENSE"} {
		if err := os.WriteFile(filepath.Join(programDir, name), []byte("old "+name), 0700); err != nil {
			t.Fatal(err)
		}
	}

	// New files in stageDir
	files := []string{"passage.exe", "passage-iroh.exe", "passage-cli.exe", "LICENSE"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(stageDir, name), []byte("new "+name), 0700); err != nil {
			t.Fatal(err)
		}
	}

	plans := planReplacements(programDir, stageDir, files)
	restore, err := executeReplacements(plans)
	if err != nil {
		t.Fatalf("replacement failed: %v", err)
	}

	// Verify new files are in place
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(programDir, name))
		if err != nil {
			t.Fatalf("failed reading replaced file %s: %v", name, err)
		}
		if string(data) != "new "+name {
			t.Fatalf("file %s has unexpected content: %s", name, string(data))
		}
	}

	// Verify backups exist for previously existing files
	for _, name := range []string{"passage.exe", "passage-iroh.exe", "LICENSE"} {
		data, err := os.ReadFile(filepath.Join(programDir, name+".old"))
		if err != nil {
			t.Fatalf("backup %s.old missing: %v", name, err)
		}
		if string(data) != "old "+name {
			t.Fatalf("backup %s.old has wrong content: %s", name, string(data))
		}
	}

	// Now invoke restore and verify rollback
	if err := restore(); err != nil {
		t.Fatalf("restore returned error: %v", err)
	}

	for _, name := range []string{"passage.exe", "passage-iroh.exe", "LICENSE"} {
		data, err := os.ReadFile(filepath.Join(programDir, name))
		if err != nil {
			t.Fatalf("restored file %s missing: %v", name, err)
		}
		if string(data) != "old "+name {
			t.Fatalf("restored file %s has wrong content: %s", name, string(data))
		}
	}

	// passage-cli.exe was not pre-existing, so it should be gone after restore
	if _, err := os.Stat(filepath.Join(programDir, "passage-cli.exe")); !os.IsNotExist(err) {
		t.Fatal("expected non-pre-existing file passage-cli.exe to be removed on restore")
	}
}

func TestRollbackUpdate(t *testing.T) {
	programDir := t.TempDir()

	for _, name := range []string{"passage.exe", "passage-iroh.exe"} {
		os.WriteFile(filepath.Join(programDir, name), []byte("new"), 0700)
		os.WriteFile(filepath.Join(programDir, name+".old"), []byte("original "+name), 0700)
	}

	if err := rollbackUpdate(programDir); err != nil {
		t.Fatalf("rollbackUpdate failed: %v", err)
	}

	for _, name := range []string{"passage.exe", "passage-iroh.exe"} {
		data, err := os.ReadFile(filepath.Join(programDir, name))
		if err != nil || string(data) != "original "+name {
			t.Fatalf("file %s not restored correctly: %v, content: %s", name, err, string(data))
		}
	}
}

func TestCleanLeftovers(t *testing.T) {
	programDir := t.TempDir()

	os.WriteFile(filepath.Join(programDir, "passage.exe"), []byte("app"), 0700)
	os.WriteFile(filepath.Join(programDir, "passage.exe.old"), []byte("old app"), 0700)
	os.WriteFile(filepath.Join(programDir, "passage.ini"), []byte("settings"), 0600)
	os.WriteFile(filepath.Join(programDir, "link.txt"), []byte("link"), 0600)

	tempStage := filepath.Join(programDir, ".passage-update-12345")
	os.MkdirAll(tempStage, 0700)
	os.WriteFile(filepath.Join(tempStage, "staged.exe"), []byte("temp"), 0700)

	if err := cleanLeftovers(programDir); err != nil {
		t.Fatalf("cleanLeftovers failed: %v", err)
	}

	// .old and .passage-update-* should be removed
	if _, err := os.Stat(filepath.Join(programDir, "passage.exe.old")); !os.IsNotExist(err) {
		t.Fatal("passage.exe.old was not removed")
	}
	if _, err := os.Stat(tempStage); !os.IsNotExist(err) {
		t.Fatal("temp stage dir was not removed")
	}

	// App files, settings, and links must remain
	for _, name := range []string{"passage.exe", "passage.ini", "link.txt"} {
		if _, err := os.Stat(filepath.Join(programDir, name)); err != nil {
			t.Fatalf("essential file %s was removed: %v", name, err)
		}
	}
}

func TestAwaitHealth(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	open := make(chan struct{})
	if !awaitHealth(closed, open, time.Minute) {
		t.Fatal("health signal ignored")
	}
	if awaitHealth(open, closed, time.Minute) {
		t.Fatal("exit without a health signal accepted")
	}
	if !awaitHealth(closed, closed, time.Minute) {
		t.Fatal("exit after a health signal rejected")
	}
	if awaitHealth(open, open, time.Millisecond) {
		t.Fatal("timeout accepted")
	}
}
