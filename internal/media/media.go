// Package media is a registry of file-extension classes. Drivers look up
// classes by name (e.g. "raw", "fujifilm-image") to classify files for copy
// routing. Classes are registered once at startup; tests use a fresh
// Registry.
package media

import (
	"path/filepath"
	"strings"
	"sync"
)

// RAW extension list from the bash usbimport script.
var rawExts = []string{
	"3fr", "arw", "cr2", "cr3", "dcr", "dng", "erf", "gpr", "iiq", "kdc",
	"mef", "mos", "mrw", "nef", "nrw", "orf", "pef", "raf", "raw", "rw2",
	"rwl", "sr2", "srf", "srw", "x3f",
}

var imageExts = []string{"jpg", "jpeg"}
var videoExts = []string{"mov", "mp4", "avi"}

// Registry holds a set of named extension classes.
type Registry struct {
	mu      sync.RWMutex
	classes map[string]map[string]struct{}
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{classes: map[string]map[string]struct{}{}}
}

// RegisterClass adds exts to the named class. Extensions are normalized to
// lowercase with a leading dot.
func (r *Registry) RegisterClass(name string, exts ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := strings.ToLower(name)
	set, ok := r.classes[key]
	if !ok {
		set = map[string]struct{}{}
		r.classes[key] = set
	}
	for _, e := range exts {
		set[normExt(e)] = struct{}{}
	}
}

// IsA reports whether filename's extension is in the named class.
func (r *Registry) IsA(class, filename string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set, ok := r.classes[strings.ToLower(class)]
	if !ok {
		return false
	}
	_, found := set[normExt(filepath.Ext(filename))]
	return found
}

// Classes lists the classes that include filename.
func (r *Registry) Classes(filename string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ext := normExt(filepath.Ext(filename))
	var out []string
	for name, set := range r.classes {
		if _, ok := set[ext]; ok {
			out = append(out, name)
		}
	}
	return out
}

func normExt(e string) string {
	if e == "" {
		return ""
	}
	if !strings.HasPrefix(e, ".") {
		e = "." + e
	}
	return strings.ToLower(e)
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default returns the singleton registry populated with the built-in classes.
// Lazily initialized; safe for concurrent use.
func Default() *Registry {
	defaultOnce.Do(func() {
		defaultReg = NewRegistry()
		RegisterDefaults(defaultReg)
	})
	return defaultReg
}

// RegisterDefaults registers the built-in media classes on r:
// "raw", "image", "video", "fujifilm-media", "fujifilm-image".
func RegisterDefaults(r *Registry) {
	r.RegisterClass("raw", rawExts...)
	r.RegisterClass("image", imageExts...)
	r.RegisterClass("video", videoExts...)
	// fujifilm-media = raw ∪ image ∪ video
	media := append(append([]string{}, rawExts...), append(imageExts, videoExts...)...)
	r.RegisterClass("fujifilm-media", media...)
	// fujifilm-image = raw ∪ image
	fimg := append(append([]string{}, rawExts...), imageExts...)
	r.RegisterClass("fujifilm-image", fimg...)
}
