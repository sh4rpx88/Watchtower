package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

// maxFileSize caps file reads and writes through the panel. Bigger files
// belong in backups or on the machine itself.
const maxFileSize = 2 << 20

// resolve turns a path from the panel into an absolute path inside the
// instance's folder. Symlinks are followed and must stay inside too, so a
// link planted by a game mod can't expose the rest of the machine.
func (m *Manager) resolve(id, rel string) (string, error) {
	if !validID.MatchString(id) {
		return "", errors.New("invalid instance id")
	}
	base, err := filepath.EvalSymlinks(m.dir(id))
	if err != nil {
		return "", errors.New("the server has no files yet; install it first")
	}
	p := filepath.Join(base, filepath.Clean("/"+rel))
	// The target may not exist yet (write, mkdir); check its nearest existing parent.
	check := p
	for {
		real, err := filepath.EvalSymlinks(check)
		if err == nil {
			if real != base && !strings.HasPrefix(real, base+string(filepath.Separator)) {
				return "", errors.New("path is outside the server folder")
			}
			return p, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		check = filepath.Dir(check)
	}
}

func (m *Manager) ListFiles(req protocol.FileRequest) ([]protocol.FileInfo, error) {
	p, err := m.resolve(req.ID, req.Path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := []protocol.FileInfo{}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, protocol.FileInfo{Name: e.Name(), Dir: e.IsDir(), Size: info.Size(), ModTime: info.ModTime().Unix()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (m *Manager) ReadFile(req protocol.FileRequest) (protocol.FileContent, error) {
	p, err := m.resolve(req.ID, req.Path)
	if err != nil {
		return protocol.FileContent{}, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return protocol.FileContent{}, err
	}
	if info.IsDir() {
		return protocol.FileContent{}, errors.New("that's a folder")
	}
	if info.Size() > maxFileSize {
		return protocol.FileContent{}, fmt.Errorf("file is too big to open here (over %d MB)", maxFileSize>>20)
	}
	data, err := os.ReadFile(p)
	return protocol.FileContent{ID: req.ID, Path: req.Path, Content: data}, err
}

func (m *Manager) WriteFile(req protocol.FileContent) error {
	if len(req.Content) > maxFileSize {
		return fmt.Errorf("file is too big (over %d MB)", maxFileSize>>20)
	}
	p, err := m.resolve(req.ID, req.Path)
	if err != nil {
		return err
	}
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		return errors.New("that's a folder")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".watchtower-tmp"
	if err := os.WriteFile(tmp, req.Content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (m *Manager) DeleteFile(req protocol.FileRequest) error {
	p, err := m.resolve(req.ID, req.Path)
	if err != nil {
		return err
	}
	if p == filepath.Clean(m.dir(req.ID)) || filepath.Clean("/"+req.Path) == "/" {
		return errors.New("can't delete the server folder itself")
	}
	return os.RemoveAll(p)
}

func (m *Manager) Mkdir(req protocol.FileRequest) error {
	p, err := m.resolve(req.ID, req.Path)
	if err != nil {
		return err
	}
	return os.MkdirAll(p, 0o755)
}
