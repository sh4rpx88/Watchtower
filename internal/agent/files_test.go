package agent

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

const testID = "abcdef0123456789"

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(t.TempDir(), nil)
	if err := os.MkdirAll(m.dir(testID), 0o755); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFilesStayInsideInstance(t *testing.T) {
	m := newTestManager(t)
	if err := m.WriteFile(protocol.FileContent{ID: testID, Path: "config/server.properties", Content: []byte("motd=hi\n")}); err != nil {
		t.Fatal(err)
	}
	got, err := m.ReadFile(protocol.FileRequest{ID: testID, Path: "/config/server.properties"})
	if err != nil || string(got.Content) != "motd=hi\n" {
		t.Fatalf("read back %q, %v", got.Content, err)
	}
	list, err := m.ListFiles(protocol.FileRequest{ID: testID, Path: ""})
	if err != nil || len(list) != 1 || list[0].Name != "config" || !list[0].Dir {
		t.Fatalf("list: %+v, %v", list, err)
	}

	// ".." is cleaned away and can't climb out.
	if err := m.WriteFile(protocol.FileContent{ID: testID, Path: "../../escape.txt", Content: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.dir(testID), "escape.txt")); err != nil {
		t.Fatalf("expected the write to land inside the instance: %v", err)
	}

	// A symlink pointing outside is refused, for reading and for writing through it.
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644)
	os.Symlink(outside, filepath.Join(m.dir(testID), "link"))
	if _, err := m.ReadFile(protocol.FileRequest{ID: testID, Path: "link/secret"}); err == nil {
		t.Fatal("read through an outside symlink should fail")
	}
	if err := m.WriteFile(protocol.FileContent{ID: testID, Path: "link/new", Content: []byte("x")}); err == nil {
		t.Fatal("write through an outside symlink should fail")
	}

	if err := m.DeleteFile(protocol.FileRequest{ID: testID, Path: "/"}); err == nil {
		t.Fatal("deleting the root should fail")
	}
}

func TestBackupAndRestore(t *testing.T) {
	m := newTestManager(t)
	dir := m.dir(testID)
	os.MkdirAll(filepath.Join(dir, "world"), 0o755)
	os.WriteFile(filepath.Join(dir, "world", "level.dat"), []byte("original"), 0o644)
	m.instances[testID] = &instance{state: protocol.StateStopped}

	if err := m.CreateBackup(protocol.InstanceSpec{ID: testID}); err != nil {
		t.Fatal(err)
	}
	var backups []protocol.BackupInfo
	waitFor(t, func() bool {
		backups, _ = m.ListBackups(testID)
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(backups) == 1 && m.instances[testID].busy == ""
	})

	os.WriteFile(filepath.Join(dir, "world", "level.dat"), []byte("griefed"), 0o644)
	os.WriteFile(filepath.Join(dir, "junk"), []byte("x"), 0o644)
	if err := m.RestoreBackup(protocol.BackupRequest{ID: testID, Name: backups[0].Name}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.instances[testID].busy == ""
	})
	data, _ := os.ReadFile(filepath.Join(dir, "world", "level.dat"))
	if string(data) != "original" {
		t.Fatalf("level.dat = %q after restore", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "junk")); !os.IsNotExist(err) {
		t.Fatal("files added after the backup should be gone")
	}

	if err := m.RestoreBackup(protocol.BackupRequest{ID: testID, Name: "../../etc.tar.gz"}); err == nil {
		t.Fatal("bad backup names should be refused")
	}
	if err := m.DeleteBackup(protocol.BackupRequest{ID: testID, Name: backups[0].Name}); err != nil {
		t.Fatal(err)
	}
}

func TestExtractRefusesTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, _ := os.Create(archive)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "../../evil", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	f.Close()

	dest := filepath.Join(t.TempDir(), "out")
	if err := extractArchive(archive, dest); err != nil {
		t.Fatal(err) // the path is cleaned to dest/evil, which is safe
	}
	if _, err := os.Stat(filepath.Join(dest, "evil")); err != nil {
		t.Fatalf("expected the entry inside dest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(dest)), "evil")); !os.IsNotExist(err) {
		t.Fatal("entry escaped dest")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
