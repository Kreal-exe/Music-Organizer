package device

// Playlists on the phone: finding them, saying how much of each the phone
// really shows, and putting a new one in place.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"musiclibraryorganizer/internal/playlist"
)

// Storage is the phone's shared storage under the name its media database
// uses.
const Storage = "/storage/emulated/0"

// PlaylistFile is a playlist file found on the phone.
type PlaylistFile struct {
	Path string `json:"path"`
	// Visible is how many of its tracks the phone's media database matched —
	// what Phonograph and most players show — or -1 when the database does
	// not list the file.
	Visible int `json:"visible"`
}

// lines splits a command's output into its lines, keeping each one's bytes —
// trailing spaces included, since a file name may end in one.
func lines(out []byte) []string {
	var all []string
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if line = strings.TrimSuffix(line, "\r"); line != "" {
			all = append(all, line)
		}
	}
	return all
}

// MediaFiles lists the music under a folder on the phone, as paths relative to
// it spelled exactly as the phone names the files. That spelling is what a
// playlist has to use: "й" written as one code point and as two are two
// different names to Android, though they look the same everywhere.
func (p *Phone) MediaFiles(ctx context.Context, dir string) ([]string, error) {
	dir = strings.TrimSuffix(dir, "/")
	out, err := p.run(ctx, "exec-out", "find "+shellQuote(dir+"/")+" -type f 2>/dev/null")
	if err != nil {
		return nil, err
	}
	prefix := dir + "/"
	var files []string
	for _, line := range lines(out) {
		rel, found := strings.CutPrefix(line, prefix)
		if !found || !playlist.IsMediaName(rel) || strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.") {
			continue
		}
		files = append(files, rel)
	}
	return files, nil
}

// Playlists finds the playlist files in the phone's storage, with how many of
// their tracks the phone shows.
func (p *Phone) Playlists(ctx context.Context) ([]PlaylistFile, error) {
	out, err := p.run(ctx, "exec-out", "find "+Storage+"/ -type f "+
		`\( -iname '*.m3u' -o -iname '*.m3u8' -o -iname '*.pls' -o -iname '*.wpl' -o -iname '*.xspf' \) 2>/dev/null`)
	if err != nil {
		return nil, err
	}
	visible := p.visibleCounts(ctx)

	files := []PlaylistFile{}
	for _, path := range lines(out) {
		// Other applications' private folders, and what a file manager keeps
		// in its own bin.
		if strings.Contains(path, "/Android/data/") || strings.Contains(path, "/Android/obb/") || strings.Contains(path, "/.") {
			continue
		}
		count, known := visible[path]
		if !known {
			count = -1
		}
		files = append(files, PlaylistFile{Path: path, Visible: count})
	}
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Path) < strings.ToLower(files[j].Path) })
	return files, nil
}

// visibleCounts says, for every playlist file the media database knows, how
// many of its tracks the database matched. All of them are counted on the
// phone in one command: each query takes about a second to start.
func (p *Phone) visibleCounts(ctx context.Context) map[string]int {
	ids := p.playlistIDs(ctx)
	byID := map[string]string{}
	for path, id := range ids {
		if strings.HasPrefix(path, Storage) {
			byID[id] = path
		}
	}
	if len(byID) == 0 {
		return map[string]int{}
	}
	list := make([]string, 0, len(byID))
	for id := range byID {
		list = append(list, id)
	}
	script := "for i in " + strings.Join(list, " ") + "; do echo \"$i $(content query --uri " +
		"content://media/external/audio/playlists/$i/members --projection audio_id | grep -c '^Row')\"; done"
	out, err := p.run(ctx, "exec-out", script)
	if err != nil {
		return map[string]int{}
	}
	counts := map[string]int{}
	for _, line := range lines(out) {
		id, n, found := strings.Cut(strings.TrimSpace(line), " ")
		path, known := byID[id]
		if !found || !known {
			continue
		}
		// The database can list one file twice; the fuller entry is the
		// current one.
		count := number([]byte(n))
		counts[path] = max(counts[path], count)
		counts[strings.Replace(path, Storage, "/sdcard", 1)] = counts[path]
	}
	return counts
}

// ReadFile copies a small file off the phone.
func (p *Phone) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return p.run(ctx, "exec-out", "cat "+shellQuote(path))
}

// WritePlaylist puts a playlist file on the phone and has the media database
// read it, then reports how many of its tracks the database matched (-1 when
// it could not tell). A different file already there is kept beside it as
// .bak.
func (p *Phone) WritePlaylist(ctx context.Context, path string, data []byte, entries int) (int, error) {
	local, err := os.CreateTemp("", "mlm-playlist-*")
	if err != nil {
		return -1, err
	}
	defer os.Remove(local.Name())
	if _, err := local.Write(data); err != nil {
		local.Close()
		return -1, err
	}
	if err := local.Close(); err != nil {
		return -1, err
	}

	if old, err := p.ReadFile(ctx, path); err == nil && len(old) > 0 && !bytes.Equal(old, data) {
		if _, err := p.run(ctx, "shell", "cp -f "+shellQuote(path)+" "+shellQuote(path+".bak")); err != nil {
			return -1, fmt.Errorf("could not keep the old playlist: %w", err)
		}
	}
	if _, err := p.run(ctx, "push", local.Name(), path); err != nil {
		return -1, fmt.Errorf("could not copy the playlist to the phone: %w", err)
	}

	// The database reads a playlist in its own time, and an early count comes
	// back short; it is asked again until the count stops growing.
	stored := strings.Replace(path, "/sdcard/", Storage+"/", 1)
	best := -1
	for attempt := range reimportTries + 1 {
		if attempt > 0 && !pause(ctx, reimportPause) {
			break
		}
		if attempt == 0 || best < entries {
			p.scanFile(ctx, path)
		}
		if !pause(ctx, time.Second) {
			break
		}
		id := p.playlistIDs(ctx)[stored]
		if id == "" {
			continue
		}
		got := p.countMembers(ctx, id)
		if got >= entries || (best >= 0 && got <= best) {
			return max(got, best), nil
		}
		best = got
	}
	return best, nil
}
