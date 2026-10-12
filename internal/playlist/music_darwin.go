package playlist

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// musicScript asks Music for every playlist the user made, smart ones
// included, with the file, name, artist and length of each track. The
// properties are read for a whole playlist at a time: one by one, a library
// of a few thousand tracks takes minutes.
const musicScript = `
const music = Application('Music');
const out = [];
for (const p of music.userPlaylists()) {
  let kind = 'none';
  try { kind = p.specialKind(); } catch (e) {}
  if (kind !== 'none') continue;
  const tracks = p.fileTracks;
  const list = { name: p.name(), locations: [], titles: [], artists: [], durations: [] };
  try {
    list.locations = tracks.location().map((l) => (l ? l.toString() : ''));
    list.titles = tracks.name();
    list.artists = tracks.artist();
    list.durations = tracks.duration();
  } catch (e) {}
  out.push(list);
}
JSON.stringify(out);
`

// FromMusicApp reads the playlists of the Music application — iTunes, on
// older Macs — straight from it, so nothing has to be exported by hand. It
// starts Music if it is not running, and macOS asks once whether this
// application may control it.
func FromMusicApp(ctx context.Context) ([]Playlist, error) {
	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-")
	cmd.Stdin = strings.NewReader(musicScript)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "-1743") {
			return nil, fmt.Errorf("this application may not control Music — allow it under System Settings → Privacy & Security → Automation")
		}
		return nil, fmt.Errorf("could not read the playlists in Music: %s", strings.TrimSpace(stderr.String()))
	}

	var lists []struct {
		Name      string    `json:"name"`
		Locations []string  `json:"locations"`
		Titles    []string  `json:"titles"`
		Artists   []string  `json:"artists"`
		Durations []float64 `json:"durations"`
	}
	if err := json.Unmarshal(out, &lists); err != nil {
		return nil, fmt.Errorf("Music answered something unreadable: %w", err)
	}

	playlists := []Playlist{}
	for _, list := range lists {
		entries := []Entry{}
		for i, location := range list.Locations {
			entry := Entry{Location: location, Duration: -1}
			if i < len(list.Titles) {
				entry.Title = list.Titles[i]
			}
			if i < len(list.Artists) {
				entry.Artist = list.Artists[i]
			}
			if i < len(list.Durations) && list.Durations[i] > 0 {
				entry.Duration = int(list.Durations[i] + 0.5)
			}
			// A track whose file Music has lost has no location; it still
			// counts, as one that cannot be found.
			if entry.Location == "" {
				entry.Location = entry.Title
			}
			entries = append(entries, entry)
		}
		if len(entries) == 0 {
			continue
		}
		playlists = append(playlists, Playlist{Name: strings.TrimSpace(list.Name), Format: "Music", Entries: entries})
	}
	return playlists, nil
}
