package playlist

import (
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Index is the music a playlist's entries are looked up among: a folder on
// this computer or on a phone.
//
// An entry is found by its path when that file exists. Otherwise it is found
// by the longest tail of its path that names exactly one file here — which is
// what lets a playlist made under another user, on another drive or volume,
// or on another computer altogether still find its tracks. Names are compared
// without regard to letter case or Unicode form, as Android's and Windows'
// file systems compare them, but every path handed back is spelled as the file
// really is named, byte for byte.
type Index struct {
	// Root is the folder, with forward slashes.
	Root  string
	local bool

	rootKey string
	exact   map[string]string   // key of a relative path → the path as stored
	tails   map[string][]string // key of the last 1, 2, 3… parts → the paths
}

// IndexFolder lists the music and video files under a folder on this
// computer. Hidden files and folders are left out.
func IndexFolder(root string) (*Index, error) {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	var relative []string
	err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			if file == root {
				return err
			}
			return nil // An unreadable folder inside is skipped, not fatal.
		}
		if file != root && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() && IsMediaName(entry.Name()) {
			if rel, err := filepath.Rel(root, file); err == nil {
				relative = append(relative, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return NewIndex(filepath.ToSlash(root), relative, true), nil
}

// NewIndex makes an index of files listed elsewhere, such as on a phone:
// relative holds their paths under root, spelled as listed there. local says
// whether the files can also be looked at directly, on this computer.
func NewIndex(root string, relative []string, local bool) *Index {
	root = trimSlash(root)
	ix := &Index{
		Root:    root,
		local:   local,
		rootKey: key(root),
		exact:   make(map[string]string, len(relative)),
		tails:   make(map[string][]string, len(relative)*3),
	}
	for _, rel := range relative {
		k := key(rel)
		if _, seen := ix.exact[k]; seen {
			continue
		}
		ix.exact[k] = rel
		parts := strings.Split(k, "/")
		for n := 1; n <= len(parts); n++ {
			tail := strings.Join(parts[len(parts)-n:], "/")
			ix.tails[tail] = append(ix.tails[tail], rel)
		}
	}
	return ix
}

// Len is the number of files in the index.
func (ix *Index) Len() int { return len(ix.exact) }

// key is how names are compared: Unicode form and letter case do not matter.
func key(path string) string {
	return strings.ToLower(norm.NFC.String(path))
}

func trimSlash(path string) string {
	for len(path) > 1 && strings.HasSuffix(path, "/") {
		path = path[:len(path)-1]
	}
	return path
}

func (ix *Index) absolute(rel string) string {
	if ix.Root == "/" {
		return "/" + rel
	}
	return ix.Root + "/" + rel
}

// indexed returns the file at path when it lies under the root and is one of
// the files listed.
func (ix *Index) indexed(path string) (string, bool) {
	k := key(path)
	prefix := ix.rootKey + "/"
	if ix.rootKey == "/" {
		prefix = "/"
	}
	if !strings.HasPrefix(k, prefix) {
		return "", false
	}
	rel, ok := ix.exact[k[len(prefix):]]
	if !ok {
		return "", false
	}
	return ix.absolute(rel), true
}

// Resolve finds the file an entry's location refers to. folder is where the
// playlist lies, for relative entries.
func (ix *Index) Resolve(location, folder string) (string, bool) {
	p := pathFromLocation(location)
	if p == "" {
		return "", false
	}

	// 1. Where the playlist says, when that file is here.
	for _, candidate := range ix.candidates(p, folder) {
		if found, ok := ix.indexed(candidate); ok {
			return found, true
		}
		if !ix.local {
			continue
		}
		native := filepath.FromSlash(candidate)
		if info, err := os.Stat(native); err == nil && info.Mode().IsRegular() {
			if resolved, err := filepath.EvalSymlinks(native); err == nil {
				native = resolved
			}
			slashed := filepath.ToSlash(native)
			if found, ok := ix.indexed(slashed); ok {
				return found, true
			}
			return slashed, true
		}
	}

	// 2. The longest tail of the path that names one file here. Several files
	// with the same name and nothing more to tell them apart: better none than
	// the wrong one.
	parts := components(key(p))
	for n := len(parts); n >= 1; n-- {
		found := ix.tails[strings.Join(parts[len(parts)-n:], "/")]
		if len(found) == 1 || (len(found) > 1 && n > 1) {
			return ix.absolute(found[0]), true
		}
	}
	return "", false
}

// candidates are the paths a location may mean literally.
func (ix *Index) candidates(p, folder string) []string {
	windows := isWindowsPath(p)
	if windows && !(ix.local && runtime.GOOS == "windows") {
		return nil // A path on some Windows computer; only its tail can help.
	}
	slashed := strings.ReplaceAll(p, `\`, "/")
	var out []string
	switch {
	case windows:
		out = append(out, clean(slashed))
	case strings.HasPrefix(slashed, "/"):
		out = append(out, clean(slashed))
		// A phone's storage goes by several names.
		for _, alias := range []string{"/sdcard/", "/mnt/sdcard/", "/storage/self/primary/"} {
			if rest, found := strings.CutPrefix(slashed, alias); found {
				out = append(out, clean("/storage/emulated/0/"+rest))
			}
		}
	default:
		if folder != "" {
			out = append(out, clean(folder+"/"+slashed))
		}
		// iTunes and Music write "Volume/Users/me/Music/a.mp3".
		if ix.local && runtime.GOOS == "darwin" {
			if _, rest, found := strings.Cut(slashed, "/"); found {
				out = append(out, clean("/"+rest), clean("/Volumes/"+slashed))
			}
		}
	}
	return out
}

// clean folds "." and ".." away without touching the disk, since the path may
// be a phone's. A leading drive letter is kept.
func clean(p string) string {
	prefix := ""
	if len(p) >= 2 && p[1] == ':' {
		prefix, p = p[:2], p[2:]
	}
	var parts []string
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "", ".":
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, part)
		}
	}
	return prefix + "/" + strings.Join(parts, "/")
}

// components splits a path whichever separator it uses; an old Mac path
// ("Disk:Users:me:a.mp3") is split at its colons.
func components(p string) []string {
	separators := "/\\"
	if !strings.ContainsAny(p, separators) {
		separators = ":"
	}
	var parts []string
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return strings.ContainsRune(separators, r) }) {
		if part != "." && part != ".." {
			parts = append(parts, part)
		}
	}
	return parts
}

// isWindowsPath says whether p is "C:\Music\a.mp3", "C:/Music/a.mp3" or a
// network path.
func isWindowsPath(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && isLetter(p[0]) && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

func isLetter(b byte) bool { return b|0x20 >= 'a' && b|0x20 <= 'z' }

func hasFileScheme(location string) bool {
	return len(location) >= 5 && strings.EqualFold(location[:5], "file:")
}

// pathFromLocation turns a file URL into a path ("file:///C:/Music/a%20b.mp3"
// becomes "C:/Music/a b.mp3"); anything else is already one.
func pathFromLocation(location string) string {
	if !hasFileScheme(location) {
		return location
	}
	p := location[5:]
	if rest, found := strings.CutPrefix(p, "//localhost/"); found {
		p = "/" + rest
	} else if rest, found := strings.CutPrefix(p, "//"); found {
		p = rest
	}
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}
	if len(p) >= 4 && p[0] == '/' && isWindowsPath(p[1:]) {
		p = p[1:]
	}
	return p
}

// Find looks up every entry of a playlist. Entries whose file is nowhere go to
// missing, in order.
func (ix *Index) Find(list Playlist) (found []Track, missing []Entry) {
	for _, entry := range list.Entries {
		file, ok := ix.Resolve(entry.Location, list.Folder)
		if trimmed := strings.TrimSpace(entry.Location); !ok && trimmed != entry.Location {
			file, ok = ix.Resolve(trimmed, list.Folder)
		}
		if !ok {
			missing = append(missing, entry)
			continue
		}
		found = append(found, Track{Path: file, Entry: entry})
	}
	return found, missing
}
