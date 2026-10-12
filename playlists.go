package main

// Playlists: reading them from iTunes, Android and desktop players, finding
// their tracks in the open library or on a phone, and writing them for
// another player.
//
// The playlists read are kept here by id, so the interface only ever sends ids
// and names back; the open library is what their tracks are looked up in.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	wr "github.com/wailsapp/wails/v2/pkg/runtime"

	"musiclibraryorganizer/internal/device"
	"musiclibraryorganizer/internal/playlist"
)

// playlists is what the Playlists view has loaded.
type playlists struct {
	mu    sync.Mutex
	next  int
	read  map[string]playlist.Playlist
	index *playlist.Index // the open library's files; nil until first needed
	of    string          // the library root the index was made for
}

// PlaylistItem is a playlist as the interface lists it.
type PlaylistItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Format string `json:"format"`
	// Where is "computer" or "phone"; Path is the file it was read from.
	Where   string `json:"where"`
	Path    string `json:"path"`
	Entries int    `json:"entries"`
	// Found is how many of its tracks are in the open library.
	Found int `json:"found"`
	// Visible is how many of them the phone shows, for a playlist on a
	// phone; -1 otherwise.
	Visible int `json:"visible"`
}

// PlaylistTargets lists the players a playlist can be written for.
func (a *App) PlaylistTargets() []playlist.TargetInfo {
	return playlist.Targets
}

// libraryIndex lists the files of the open library, once per scan.
func (a *App) libraryIndex(ctx context.Context) (*playlist.Index, error) {
	a.mu.Lock()
	lib, phone := a.lib, a.phone
	a.mu.Unlock()
	if lib == nil {
		return nil, errors.New("open a library first")
	}

	a.lists.mu.Lock()
	cached, of := a.lists.index, a.lists.of
	a.lists.mu.Unlock()
	if cached != nil && of == lib.Root {
		return cached, nil
	}

	var index *playlist.Index
	if phone != nil {
		files, err := phone.MediaFiles(ctx, lib.Root)
		if err != nil {
			return nil, err
		}
		index = playlist.NewIndex(lib.Root, files, false)
	} else {
		var err error
		if index, err = playlist.IndexFolder(lib.Root); err != nil {
			return nil, err
		}
	}

	a.lists.mu.Lock()
	a.lists.index, a.lists.of = index, lib.Root
	a.lists.mu.Unlock()
	return index, nil
}

// keep remembers playlists read and describes them for the interface.
func (a *App) keep(ctx context.Context, read []playlist.Playlist, where, file string, visible int) []PlaylistItem {
	index, _ := a.libraryIndex(ctx)

	a.lists.mu.Lock()
	defer a.lists.mu.Unlock()
	if a.lists.read == nil {
		a.lists.read = map[string]playlist.Playlist{}
	}
	items := make([]PlaylistItem, 0, len(read))
	for _, list := range read {
		a.lists.next++
		id := strconv.Itoa(a.lists.next)
		a.lists.read[id] = list
		item := PlaylistItem{ID: id, Name: list.Name, Format: list.Format, Where: where, Path: file,
			Entries: len(list.Entries), Found: -1, Visible: visible}
		if index != nil {
			found, _ := index.Find(list)
			item.Found = len(found)
		}
		items = append(items, item)
	}
	return items
}

// ChoosePlaylistFiles asks for playlist files and reads them.
func (a *App) ChoosePlaylistFiles() ([]PlaylistItem, error) {
	a.mu.Lock()
	start := ""
	if a.lib != nil && a.phone == nil {
		start = a.lib.Root
	}
	a.mu.Unlock()

	chosen, err := wr.OpenMultipleFilesDialog(a.ctx, wr.OpenDialogOptions{
		Title:            "Choose playlists",
		DefaultDirectory: start,
		Filters: []wr.FileFilter{{
			DisplayName: "Playlists (iTunes .txt and .xml, .m3u, .m3u8, .pls, .xspf, .wpl)",
			Pattern:     "*.txt;*.xml;*.m3u;*.m3u8;*.pls;*.xspf;*.wpl;*.zpl",
		}},
	})
	if err != nil || len(chosen) == 0 {
		return []PlaylistItem{}, err
	}
	return a.readFiles(chosen)
}

func (a *App) readFiles(files []string) ([]PlaylistItem, error) {
	items := []PlaylistItem{}
	var unread []string
	for _, file := range files {
		read, err := playlist.Read(file)
		if err != nil || len(read) == 0 {
			unread = append(unread, filepath.Base(file))
			continue
		}
		items = append(items, a.keep(a.ctx, read, "computer", file, -1)...)
	}
	if len(items) == 0 && len(unread) > 0 {
		return items, fmt.Errorf("not a playlist, or a format this does not read: %s", strings.Join(unread, ", "))
	}
	return items, nil
}

// LibraryPlaylists finds the playlists inside the open library: the playlist
// files in its folder, or every playlist on the phone it is on.
func (a *App) LibraryPlaylists() ([]PlaylistItem, error) {
	a.mu.Lock()
	lib, phone := a.lib, a.phone
	a.mu.Unlock()
	if lib == nil {
		return nil, errors.New("open a library first")
	}
	if phone != nil {
		return a.phonePlaylists(phone)
	}

	var files []string
	_ = filepath.WalkDir(lib.Root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if file != lib.Root && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() && playlist.IsPlaylistName(entry.Name()) {
			files = append(files, file)
		}
		return nil
	})
	items := []PlaylistItem{}
	for _, file := range files {
		// A text file that is not an iTunes export is simply not a playlist.
		if read, err := playlist.Read(file); err == nil && len(read) > 0 {
			items = append(items, a.keep(a.ctx, read, "computer", file, -1)...)
		}
	}
	return items, nil
}

// PhonePlaylists reads the playlists on a phone, with how many of their
// tracks the phone shows.
func (a *App) PhonePlaylists(serial string) ([]PlaylistItem, error) {
	a.mu.Lock()
	phone := a.phone
	a.mu.Unlock()
	if phone == nil || phone.Serial != serial {
		var err error
		if phone, err = a.connect(serial); err != nil {
			return nil, err
		}
	}
	return a.phonePlaylists(phone)
}

func (a *App) phonePlaylists(phone *device.Phone) ([]PlaylistItem, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
	defer cancel()

	files, err := phone.Playlists(ctx)
	if err != nil {
		return nil, err
	}
	items := []PlaylistItem{}
	for _, file := range files {
		data, err := phone.ReadFile(ctx, file.Path)
		if err != nil {
			continue
		}
		read := playlist.Parse(data, path.Base(file.Path), path.Dir(file.Path))
		items = append(items, a.keep(ctx, read, "phone", file.Path, file.Visible)...)
	}
	return items, nil
}

// ChoosePlaylistFolder asks where on this computer to save playlists.
func (a *App) ChoosePlaylistFolder() (string, error) {
	return wr.OpenDirectoryDialog(a.ctx, wr.OpenDialogOptions{Title: "Save the playlists in"})
}

// PlaylistSave is what the interface asks to have written.
type PlaylistSave struct {
	Items []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"items"`
	Target playlist.Target `json:"target"`
	// Destination is "library" (beside the music: the library's folder, on
	// this computer or on its phone), "folder" (another folder on this
	// computer) or "phone" (a folder on a phone, for a library on this
	// computer).
	Destination string `json:"destination"`
	Folder      string `json:"folder"`
	Serial      string `json:"serial"`
}

// PlaylistSaved is how one playlist came out.
type PlaylistSaved struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	File    string   `json:"file"`
	Entries int      `json:"entries"`
	Found   int      `json:"found"`
	Missing []string `json:"missing"`
	// Visible is how many tracks the phone shows once it has read the file;
	// -1 off a phone, or when the phone would not say.
	Visible int    `json:"visible"`
	Error   string `json:"error,omitempty"`
}

// SavePlaylists writes the chosen playlists for a player, each track looked
// up among the files where the playlist is going and spelled as it is named
// there.
func (a *App) SavePlaylists(request PlaylistSave) ([]PlaylistSaved, error) {
	info, ok := playlist.Info(request.Target)
	if !ok {
		return nil, fmt.Errorf("unknown player %q", request.Target)
	}
	a.mu.Lock()
	lib, libraryPhone := a.lib, a.phone
	a.mu.Unlock()
	if lib == nil {
		return nil, errors.New("open a library first")
	}
	ctx := a.begin()
	defer a.finishedPlaylists()

	phase := func(text string) { wr.EventsEmit(a.ctx, "playlists:phase", text) }

	// Where the files go, and which files the tracks are looked up among.
	var (
		phone  *device.Phone
		folder string // with forward slashes
		index  *playlist.Index
		err    error
	)
	switch request.Destination {
	case "library":
		phone, folder = libraryPhone, lib.Root
		phase("Listing the library…")
		index, err = a.libraryIndex(ctx)
	case "folder":
		if libraryPhone != nil {
			return nil, errors.New("the library is on the phone: save the playlists beside it")
		}
		if request.Folder == "" {
			return nil, errors.New("choose a folder to save in")
		}
		folder = filepath.ToSlash(request.Folder)
		phase("Listing the library…")
		index, err = a.libraryIndex(ctx)
	case "phone":
		folder = strings.TrimSuffix(strings.TrimSpace(request.Folder), "/")
		if folder == "" {
			return nil, errors.New("choose a folder on the phone")
		}
		if libraryPhone != nil && libraryPhone.Serial == request.Serial {
			phone = libraryPhone
		} else if phone, err = a.connect(request.Serial); err != nil {
			return nil, err
		}
		phase("Listing the music on the phone…")
		var files []string
		if files, err = phone.MediaFiles(ctx, folder); err == nil {
			index = playlist.NewIndex(folder, files, false)
		}
	default:
		return nil, fmt.Errorf("unknown destination %q", request.Destination)
	}
	if err != nil {
		return nil, err
	}
	if phone != nil && info.Absolute {
		return nil, fmt.Errorf("%s needs paths on a computer, so it cannot be saved on a phone", info.Title)
	}
	if phone == nil {
		if resolved, err := filepath.EvalSymlinks(filepath.FromSlash(folder)); err == nil {
			folder = filepath.ToSlash(resolved)
		}
	}

	results := []PlaylistSaved{}
	for n, item := range request.Items {
		if ctx.Err() != nil {
			break
		}
		a.lists.mu.Lock()
		list, known := a.lists.read[item.ID]
		a.lists.mu.Unlock()
		if !known {
			continue
		}
		name := playlist.SafeName(item.Name)
		if name == "" || strings.TrimSpace(item.Name) == "" {
			name = playlist.SafeName(list.Name)
		}
		phase(fmt.Sprintf("Saving %s (%d of %d)…", name, n+1, len(request.Items)))

		found, missing := index.Find(list)
		result := PlaylistSaved{ID: item.ID, Name: name, Entries: len(list.Entries), Found: len(found), Visible: -1, Missing: []string{}}
		for _, entry := range missing {
			result.Missing = append(result.Missing, entry.Location)
		}
		file := folder + "/" + name + "." + info.Extension
		result.File = file

		switch {
		case len(found) == 0:
			result.Error = "none of its tracks was found, so nothing was written"
		case phone != nil:
			data := playlist.Write(info.ID, name, found, folder, false)
			if result.Visible, err = phone.WritePlaylist(ctx, file, data, len(found)); err != nil {
				result.Error = err.Error()
			}
		default:
			result.File = filepath.FromSlash(file)
			data := playlist.Write(info.ID, name, found, folder, runtime.GOOS == "windows")
			if err := writeKeepingOld(result.File, data); err != nil {
				result.Error = err.Error()
			}
		}
		results = append(results, result)
		wr.EventsEmit(a.ctx, "playlists:saved", result)
	}
	return results, nil
}

// finishedPlaylists lets the next job start cleanly.
func (a *App) finishedPlaylists() {
	wr.EventsEmit(a.ctx, "playlists:phase", "")
}

// writeKeepingOld writes a file in one piece. A different file already there
// is kept beside it as .bak, so saving over a playlist never loses it.
func writeKeepingOld(file string, data []byte) error {
	if old, err := os.ReadFile(file); err == nil {
		if bytes.Equal(old, data) {
			return nil
		}
		if err := os.WriteFile(file+".bak", old, 0o644); err != nil {
			return fmt.Errorf("could not keep the old playlist: %w", err)
		}
	}
	temporary := file + ".part"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, file); err != nil {
		os.Remove(temporary)
		return err
	}
	return nil
}
