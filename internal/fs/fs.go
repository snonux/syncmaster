// Package fs is the seam over the local filesystem. All dest-side operations
// (stat, mkdir, remove, rename, read/write file, walk) go through FS so copier
// skip policies, note meta read/write, and geotag rollback are unit-testable
// without touching disk.
package fs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotExist is returned when a path does not exist.
var ErrNotExist = errors.New("fs: path does not exist")

// Entry describes a file or directory.
type Entry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// FS is the local-filesystem interface used throughout syncmaster. Every
// method takes a context as its first parameter so callers can cancel
// in-flight walks/stats/writes (e.g. on Ctrl+C abort); the in-memory Mem
// implementation ignores it, while OS checks ctx.Err() before each os call
// and aborts WalkDir on cancellation.
type FS interface {
	Stat(ctx context.Context, path string) (Entry, error)
	MkdirAll(ctx context.Context, path string, perm os.FileMode) error
	Remove(ctx context.Context, path string) error
	Rename(ctx context.Context, old, new string) error
	WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error
	ReadFile(ctx context.Context, path string) ([]byte, error)
	ReadDir(ctx context.Context, path string) ([]Entry, error)
	WalkDir(ctx context.Context, root string, fn func(path string, e Entry) error) error
}

// OS is the production FS backed by the os package.
type OS struct{}

var _ FS = (*OS)(nil)

// Stat wraps os.Stat.
func (OS) Stat(ctx context.Context, path string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, ErrNotExist
		}
		return Entry{}, err
	}
	return entryFromFileInfo(fi), nil
}

// MkdirAll wraps os.MkdirAll.
func (OS) MkdirAll(ctx context.Context, path string, perm os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.MkdirAll(path, perm)
}

// Remove wraps os.Remove.
func (OS) Remove(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(path)
}

// Rename wraps os.Rename.
func (OS) Rename(ctx context.Context, old, new string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(old, new)
}

// WriteFile wraps os.WriteFile.
func (OS) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

// ReadFile wraps os.ReadFile.
func (OS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil && os.IsNotExist(err) {
		return nil, ErrNotExist
	}
	return b, err
}

// ReadDir lists a directory, sorted by name.
func (OS) ReadDir(ctx context.Context, path string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	des, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotExist
		}
		return nil, err
	}
	return entriesFromDirEntries(path, des), nil
}

// WalkDir walks root recursively, invoking fn for each entry (including root).
// It aborts the walk as soon as ctx is cancelled.
func (OS) WalkDir(ctx context.Context, root string, fn func(path string, e Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return fn(p, entryFromFileInfo(fi))
	})
}

func entryFromFileInfo(fi os.FileInfo) Entry {
	return Entry{
		Name:    fi.Name(),
		IsDir:   fi.IsDir(),
		Size:    fi.Size(),
		ModTime: fi.ModTime(),
	}
}

func entriesFromDirEntries(_ string, des []os.DirEntry) []Entry {
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, entryFromFileInfo(fi))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Mem is an in-memory FS for tests. Files are stored as byte slices; dirs are
// implicit (created by WriteFile/MkdirAll). All paths are normalized with
// filepath.Clean. Mem is safe for concurrent use.
type Mem struct {
	mu    sync.Mutex
	files map[string]*memFile
	dirs  map[string]struct{}
}

var _ FS = (*Mem)(nil)

type memFile struct {
	data    []byte
	modtime time.Time
}

// NewMem returns an empty in-memory FS.
func NewMem() *Mem {
	return &Mem{files: map[string]*memFile{}, dirs: map[string]struct{}{}}
}

// Stat reports the entry for path.
func (m *Mem) Stat(ctx context.Context, path string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := filepath.Clean(path)
	if _, ok := m.dirs[p]; ok {
		return Entry{Name: filepath.Base(p), IsDir: true}, nil
	}
	if f, ok := m.files[p]; ok {
		return Entry{Name: filepath.Base(p), Size: int64(len(f.data)), ModTime: f.modtime}, nil
	}
	return Entry{}, ErrNotExist
}

// MkdirAll records a directory.
func (m *Mem) MkdirAll(ctx context.Context, path string, _ os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirs[filepath.Clean(path)] = struct{}{}
	return nil
}

// Remove deletes a file or empty dir.
func (m *Mem) Remove(ctx context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := filepath.Clean(path)
	if _, ok := m.files[p]; ok {
		delete(m.files, p)
		return nil
	}
	if _, ok := m.dirs[p]; ok {
		delete(m.dirs, p)
		return nil
	}
	return ErrNotExist
}

// Rename moves a file. The new path's parent is implicitly created.
func (m *Mem) Rename(ctx context.Context, old, new string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := filepath.Clean(old)
	n := filepath.Clean(new)
	f, ok := m.files[o]
	if !ok {
		return ErrNotExist
	}
	m.files[n] = f
	delete(m.files, o)
	m.dirs[filepath.Dir(n)] = struct{}{}
	return nil
}

// WriteFile writes data at path, recording modtime as now (callers that need a
// specific modtime should use WriteFileAt).
func (m *Mem) WriteFile(ctx context.Context, path string, data []byte, _ os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := filepath.Clean(path)
	m.files[p] = &memFile{data: append([]byte(nil), data...), modtime: time.Now()}
	m.dirs[filepath.Dir(p)] = struct{}{}
	return nil
}

// WriteFileAt writes data with an explicit modification time.
func (m *Mem) WriteFileAt(path string, data []byte, modtime time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := filepath.Clean(path)
	m.files[p] = &memFile{data: append([]byte(nil), data...), modtime: modtime}
	m.dirs[filepath.Dir(p)] = struct{}{}
}

// ReadFile reads a file.
func (m *Mem) ReadFile(ctx context.Context, path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[filepath.Clean(path)]
	if !ok {
		return nil, ErrNotExist
	}
	return append([]byte(nil), f.data...), nil
}

// ReadDir lists direct children of a directory.
func (m *Mem) ReadDir(ctx context.Context, path string) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	root := filepath.Clean(path)
	var out []Entry
	seen := map[string]bool{}
	for p := range m.files {
		if !strings.HasPrefix(p, root+string(filepath.Separator)) {
			continue
		}
		rel := strings.TrimPrefix(p, root+string(filepath.Separator))
		first := rel
		if i := strings.IndexByte(rel, filepath.Separator); i >= 0 {
			first = rel[:i]
			name := first
			key := filepath.Join(root, name)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Entry{Name: name, IsDir: true})
			continue
		}
		f := m.files[p]
		out = append(out, Entry{Name: first, Size: int64(len(f.data)), ModTime: f.modtime})
	}
	for d := range m.dirs {
		if d == root {
			continue
		}
		if !strings.HasPrefix(d, root+string(filepath.Separator)) {
			continue
		}
		rel := strings.TrimPrefix(d, root+string(filepath.Separator))
		if strings.ContainsRune(rel, filepath.Separator) {
			continue
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, Entry{Name: rel, IsDir: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// WalkDir walks the tree under root, invoking fn for every file and directory
// (including root itself).
func (m *Mem) WalkDir(ctx context.Context, root string, fn func(path string, e Entry) error) error {
	m.mu.Lock()
	paths := make([]string, 0, len(m.files)+len(m.dirs))
	for p := range m.files {
		paths = append(paths, p)
	}
	for d := range m.dirs {
		paths = append(paths, d)
	}
	m.mu.Unlock()

	all := m.entriesUnder(ctx, filepath.Clean(root), paths)
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := fn(k, all[k]); err != nil {
			return err
		}
	}
	return nil
}

// entriesUnder returns the entries at/under cleanRoot plus the intermediate
// directories needed to walk down to them, keyed by path. Root is included
// first.
func (m *Mem) entriesUnder(ctx context.Context, cleanRoot string, paths []string) map[string]Entry {
	all := map[string]Entry{}
	all[cleanRoot] = Entry{Name: filepath.Base(cleanRoot), IsDir: true}
	for _, p := range paths {
		if p != cleanRoot && !strings.HasPrefix(p, cleanRoot+string(filepath.Separator)) {
			continue
		}
		e, err := m.Stat(ctx, p)
		if err != nil {
			continue
		}
		all[p] = e
		// Ensure intermediate dirs are visited.
		for d := filepath.Dir(p); d != cleanRoot && d != "." && d != "/"; d = filepath.Dir(d) {
			if d == filepath.Dir(d) {
				break
			}
			if _, ok := all[d]; !ok {
				all[d] = Entry{Name: filepath.Base(d), IsDir: true}
			}
		}
	}
	return all
}
