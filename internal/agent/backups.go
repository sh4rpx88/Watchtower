package agent

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

var validBackupName = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}\.tar\.gz$`)

// saveWait is how long a running server gets to flush its world after the
// template's save command, before the backup reads the files.
var saveWait = 5 * time.Second

func (m *Manager) backupDir(id string) string { return filepath.Join(m.dataDir, "backups", id) }

// CreateBackup archives the instance folder in the background. A running
// server is asked to save first.
func (m *Manager) CreateBackup(spec protocol.InstanceSpec) error {
	in, err := m.get(spec)
	if err != nil {
		return err
	}
	m.mu.Lock()
	switch {
	case in.state == protocol.StateNotInstalled || in.state == protocol.StateInstalling:
		m.mu.Unlock()
		return errors.New("nothing to back up until the server is installed")
	case in.busy != "":
		busy := in.busy
		m.mu.Unlock()
		return fmt.Errorf("a %s is already in progress", busy)
	}
	in.busy = "backup"
	proc := in.proc
	m.mu.Unlock()

	go func() {
		defer m.clearBusy(spec.ID)
		out := m.console(spec.ID)
		if proc != nil && spec.Template.Save != "" {
			out("Saving the world before backup")
			proc.WriteLine(spec.Template.Save)
			time.Sleep(saveWait)
		}
		name := time.Now().UTC().Format("20060102-150405") + ".tar.gz"
		out("Backup started: " + name)
		size, err := writeArchive(m.dir(spec.ID), filepath.Join(m.backupDir(spec.ID), name))
		if err != nil {
			out("Backup failed: " + err.Error())
			return
		}
		out(fmt.Sprintf("Backup saved: %s (%.1f MB)", name, float64(size)/(1<<20)))
	}()
	return nil
}

func (m *Manager) clearBusy(id string) {
	m.mu.Lock()
	if in, ok := m.instances[id]; ok {
		in.busy = ""
	}
	m.mu.Unlock()
}

// writeArchive tars and gzips src into dest and returns the archive size.
func writeArchive(src, dest string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, err
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil // skip symlinks, sockets, and the like
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		// A server writing while we read can grow a file; copy only what the header promised.
		_, err = io.CopyN(tw, file, hdr.Size)
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(tmp)
	if err != nil {
		return 0, err
	}
	return info.Size(), os.Rename(tmp, dest)
}

func (m *Manager) ListBackups(id string) ([]protocol.BackupInfo, error) {
	if !validID.MatchString(id) {
		return nil, errors.New("invalid instance id")
	}
	entries, err := os.ReadDir(m.backupDir(id))
	if os.IsNotExist(err) {
		return []protocol.BackupInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []protocol.BackupInfo{}
	for _, e := range entries {
		if !validBackupName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		created, _ := time.Parse("20060102-150405", strings.TrimSuffix(e.Name(), ".tar.gz"))
		out = append(out, protocol.BackupInfo{Name: e.Name(), Size: info.Size(), CreatedAt: created.Unix()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

func (m *Manager) backupPath(req protocol.BackupRequest) (string, error) {
	if !validID.MatchString(req.ID) || !validBackupName.MatchString(req.Name) {
		return "", errors.New("invalid backup")
	}
	return filepath.Join(m.backupDir(req.ID), req.Name), nil
}

func (m *Manager) DeleteBackup(req protocol.BackupRequest) error {
	p, err := m.backupPath(req)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// RestoreBackup replaces the instance folder with the backup's contents in
// the background. The server must be stopped.
func (m *Manager) RestoreBackup(req protocol.BackupRequest) error {
	archive, err := m.backupPath(req)
	if err != nil {
		return err
	}
	if _, err := os.Stat(archive); err != nil {
		return errors.New("backup not found")
	}
	m.mu.Lock()
	in, ok := m.instances[req.ID]
	if !ok {
		in = &instance{state: protocol.StateStopped}
		m.instances[req.ID] = in
	}
	if in.proc != nil || in.state == protocol.StateInstalling || in.busy != "" {
		m.mu.Unlock()
		return errors.New("stop the server and wait for other jobs to finish before restoring")
	}
	in.busy = "restore"
	m.mu.Unlock()

	go func() {
		defer m.clearBusy(req.ID)
		out := m.console(req.ID)
		out("Restoring backup " + req.Name)
		if err := replaceDir(m.dir(req.ID), archive); err != nil {
			out("Restore failed: " + err.Error())
			return
		}
		out("Restore finished")
		m.setState(req.ID, protocol.StateStopped, "")
	}()
	return nil
}

// replaceDir extracts archive next to dir, then swaps it in. The old folder
// is only removed once the backup has been fully extracted.
func replaceDir(dir, archive string) error {
	staging := dir + ".restore"
	os.RemoveAll(staging)
	if err := extractArchive(archive, staging); err != nil {
		os.RemoveAll(staging)
		return err
	}
	old := dir + ".old"
	os.RemoveAll(old)
	if err := os.Rename(dir, old); err != nil && !os.IsNotExist(err) {
		os.RemoveAll(staging)
		return err
	}
	if err := os.Rename(staging, dir); err != nil {
		os.Rename(old, dir) // put the original back
		return err
	}
	return os.RemoveAll(old)
}

// extractArchive unpacks a backup into dest, refusing entries that would
// land outside it.
func extractArchive(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.Clean("/"+hdr.Name))
		if !strings.HasPrefix(target, dest+string(filepath.Separator)) {
			return fmt.Errorf("unsafe path in backup: %s", hdr.Name)
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
			w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			_, err = io.Copy(w, tr)
			if cerr := w.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			os.Chtimes(target, hdr.ModTime, hdr.ModTime)
		}
	}
}
