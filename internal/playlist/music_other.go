//go:build !darwin

package playlist

import (
	"context"
	"os"
	"path/filepath"
)

// FromMusicApp reads the playlists of iTunes for Windows from the library
// file it keeps up to date beside its library, so nothing has to be exported
// by hand. With no iTunes there is nothing, and no error.
func FromMusicApp(context.Context) ([]Playlist, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	for _, name := range []string{"iTunes Music Library.xml", "iTunes Library.xml"} {
		file := filepath.Join(home, "Music", "iTunes", name)
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		playlists := Parse(data, name, filepath.ToSlash(filepath.Dir(file)))
		for i := range playlists {
			playlists[i].Format = "iTunes"
		}
		return playlists, nil
	}
	return nil, nil
}
