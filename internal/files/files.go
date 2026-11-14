// Package files lists the local filesystem for the Files screen.
package files

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cloudnest/internal/dto"
)

type rootDef struct {
	id    string
	label string
}

var rootDefs = []rootDef{
	{"desktop", "Desktop"},
	{"documents", "Documents"},
	{"pictures", "Pictures"},
	{"downloads", "Downloads"},
	{"music", "Music"},
}

// Roots returns the user's known folders that currently exist.
func Roots() []dto.Root {
	out := make([]dto.Root, 0, len(rootDefs))
	for _, def := range rootDefs {
		if p, err := knownFolder(def.id); err == nil && p != "" {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				out = append(out, dto.Root{ID: def.id, Label: def.label, Path: p})
			}
		}
	}
	if len(out) == 0 {
		if home, err := os.UserHomeDir(); err == nil {
			out = append(out, dto.Root{ID: "home", Label: "Home", Path: home})
		}
	}
	return out
}

// List reads one directory. The result is sorted with folders first.
func List(path string) (dto.Listing, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return dto.Listing{Path: path}, err
	}
	items := make([]dto.Entry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, dto.Entry{
			Name:    e.Name(),
			Path:    filepath.Join(path, e.Name()),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().UnixNano(),
			Mime:    mimeFor(e.Name()),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return dto.Listing{Path: path, Entries: items}, nil
}

func mimeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic":
		return "image/*"
	case ".mp4", ".mkv", ".mov", ".avi", ".webm":
		return "video/*"
	case ".mp3", ".m4a", ".flac", ".wav", ".ogg":
		return "audio/*"
	case ".pdf":
		return "application/pdf"
	case ".zip", ".7z", ".rar", ".gz", ".tar":
		return "application/zip"
	default:
		return ""
	}
}
