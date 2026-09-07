package media

import (
	"sort"
	"testing"
)

func TestRegisterAndIsA(t *testing.T) {
	r := NewRegistry()
	r.RegisterClass("raw", "RAF", "nef")
	tests := []struct {
		name  string
		class string
		file  string
		want  bool
	}{
		{"raf lower", "raw", "img.raf", true},
		{"raf upper", "raw", "IMG.RAF", true},
		{"nef", "raw", "x.NEF", true},
		{"jpg not in raw", "raw", "x.jpg", false},
		{"no ext", "raw", "noext", false},
		{"unknown class", "nope", "x.raf", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.IsA(tc.class, tc.file); got != tc.want {
				t.Fatalf("IsA(%q,%q) = %v, want %v", tc.class, tc.file, got, tc.want)
			}
		})
	}
}

func TestUnknownClass(t *testing.T) {
	r := NewRegistry()
	if r.IsA("nope", "x.raf") {
		t.Fatal("unknown class should be false")
	}
}

func TestClasses(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	got := r.Classes("IMG.RAF")
	sort.Strings(got)
	// RAF is in raw, fujifilm-media, fujifilm-image, ricoh-media, ricoh-image
	want := []string{"fujifilm-image", "fujifilm-media", "raw", "ricoh-image", "ricoh-media"}
	if len(got) != len(want) {
		t.Fatalf("classes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("classes = %v, want %v", got, want)
		}
	}
}

func TestRegisterDefaultsCompositeClasses(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	tests := []struct {
		class string
		file  string
		want  bool
	}{
		{"raw", "a.cr3", true},
		{"image", "a.jpg", true},
		{"image", "a.jpeg", true},
		{"video", "a.mov", true},
		{"video", "a.mp4", true},
		{"video", "a.avi", true},
		{"fujifilm-media", "a.jpg", true},
		{"fujifilm-media", "a.mov", true},
		{"fujifilm-media", "a.raf", true},
		{"fujifilm-media", "a.txt", false},
		{"fujifilm-image", "a.jpg", true},
		{"fujifilm-image", "a.raf", true},
		{"fujifilm-image", "a.mov", false}, // video is not an image
		{"fujifilm-image", "a.avi", false},
		// The ricoh classes share the same per-group extension set as the
		// fujifilm ones (RICOH RAW is DNG, already in the raw class).
		{"ricoh-media", "a.jpg", true},
		{"ricoh-media", "a.mov", true},
		{"ricoh-media", "a.dng", true},
		{"ricoh-media", "a.raf", true},
		{"ricoh-media", "a.txt", false},
		{"ricoh-image", "a.jpg", true},
		{"ricoh-image", "a.dng", true},
		{"ricoh-image", "a.mov", false}, // video is not an image
	}
	for _, tc := range tests {
		t.Run(tc.class+"/"+tc.file, func(t *testing.T) {
			if got := r.IsA(tc.class, tc.file); got != tc.want {
				t.Fatalf("IsA(%q,%q) = %v, want %v", tc.class, tc.file, got, tc.want)
			}
		})
	}
}

func TestDefaultIsPopulated(t *testing.T) {
	if !Default().IsA("fujifilm-image", "DSC0001.RAF") {
		t.Fatal("Default should know fujifilm-image/RAF")
	}
	if !Default().IsA("ricoh-image", "R0000001.DNG") {
		t.Fatal("Default should know ricoh-image/DNG")
	}
	if !Default().IsA("ricoh-media", "R0000028.MOV") {
		t.Fatal("Default should know ricoh-media/MOV")
	}
}
