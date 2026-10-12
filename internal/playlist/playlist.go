// Package playlist moves playlists between players.
//
// A playlist is only a list of paths, and every player family wants that list
// written its own way: iTunes exports a tab-separated UTF-16 table with
// absolute paths, Android's media library reads an .m3u whose paths start from
// the playlist's own folder, Windows Media Player wants XML with backslashes.
// The paths themselves rarely survive the trip either — the playlist was made
// under another user, on another drive letter, or on a computer whose file
// names spell "й" as two code points where the phone spells it as one.
//
// So a playlist is read into a list of entries, every entry is looked up among
// the files where the playlist will be used (see Index), and the result is
// written in the format of the player it is for, with each path spelled
// exactly as that file is named there.
package playlist

import (
	"path"
	"strings"
)

// Entry is one line of a playlist, as the playlist has it.
type Entry struct {
	// Location is the path or URL exactly as written.
	Location string `json:"location"`
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
	// Duration is in seconds; -1 when the playlist does not say.
	Duration int `json:"duration"`
}

// Playlist is a playlist read from a file.
type Playlist struct {
	Name string `json:"name"`
	// Format is what it was read from: "iTunes", "M3U", "XSPF"…
	Format  string  `json:"format"`
	Entries []Entry `json:"entries"`
	// Folder is where the playlist lies, with forward slashes: relative
	// entries start from it. A folder on this computer or on a phone.
	Folder string `json:"folder"`
}

// Track is an entry whose file was found.
type Track struct {
	// Path is the file's absolute path with forward slashes, spelled exactly
	// as the file is named where it was found.
	Path  string
	Entry Entry
}

// Target is the player family a playlist is written for.
type Target string

const (
	// Android is for Phonograph, Poweramp, Samsung Music, Musicolet and the
	// rest of Android's players, all of which go through its media library:
	// .m3u, UTF-8, paths relative to the playlist, nothing else.
	Android Target = "android"
	// ITunes is for iTunes on Windows and Music on the Mac, which import the
	// same tab-separated UTF-16 table they export.
	ITunes Target = "itunes"
	// M3U8 is for VLC, foobar2000, AIMP, Winamp, MusicBee: extended .m3u8 with
	// titles and durations.
	M3U8 Target = "m3u8"
	// WPL is for Windows Media Player and Windows' Media Player app.
	WPL Target = "wpl"
	// XSPF is the XML playlist of VLC, Strawberry and Clementine.
	XSPF Target = "xspf"
	// PLS is Winamp's older format.
	PLS Target = "pls"
)

// TargetInfo describes a target for the interface.
type TargetInfo struct {
	ID        Target `json:"id"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Extension string `json:"extension"`
	// Absolute targets need real paths on this computer, so they cannot be
	// written for a phone.
	Absolute bool `json:"absolute"`
}

// Targets lists the targets in the order they are offered.
var Targets = []TargetInfo{
	{Android, "Android — Phonograph, Poweramp, Samsung Music, Musicolet…",
		".m3u with paths from the playlist's folder, the way Android's media library reads them", "m3u", false},
	{ITunes, "iTunes and Music — Mac and Windows",
		".txt, the same table they export; open it with File → Library → Import Playlist", "txt", true},
	{M3U8, "VLC, foobar2000, AIMP, Winamp, MusicBee",
		".m3u8 with each track's title and length", "m3u8", false},
	{WPL, "Windows Media Player",
		".wpl, also read by the Media Player app of Windows", "wpl", false},
	{XSPF, "Strawberry, Clementine, VLC (XSPF)",
		".xspf, the XML playlist", "xspf", false},
	{PLS, "Winamp and older players (PLS)",
		".pls with each track's title and length", "pls", false},
}

// Info returns the description of a target, and whether it is known.
func Info(target Target) (TargetInfo, bool) {
	for _, info := range Targets {
		if info.ID == target {
			return info, true
		}
	}
	return TargetInfo{}, false
}

// Extensions are the file extensions read as playlists.
var Extensions = []string{".m3u", ".m3u8", ".txt", ".xml", ".pls", ".xspf", ".wpl", ".zpl"}

// IsPlaylistName says whether a file name has a playlist's extension. A .txt
// or .xml is only a playlist if it reads as one, which Parse decides.
func IsPlaylistName(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	for _, known := range Extensions {
		if ext == known {
			return true
		}
	}
	return false
}

var mediaExtensions = map[string]bool{
	".mp3": true, ".m4a": true, ".m4b": true, ".m4p": true, ".aac": true, ".flac": true, ".alac": true,
	".wav": true, ".aif": true, ".aiff": true, ".aifc": true, ".ogg": true, ".oga": true, ".opus": true,
	".wma": true, ".ape": true, ".wv": true, ".mpc": true, ".dsf": true, ".dff": true, ".mka": true,
	".amr": true, ".mid": true, ".midi": true, ".mp4": true, ".m4v": true, ".mov": true, ".mkv": true,
	".webm": true, ".avi": true, ".3gp": true, ".wmv": true,
}

// IsMediaName says whether a file is a sound or a video a player may list —
// any of them, not only the formats whose tags this application edits.
func IsMediaName(name string) bool {
	return mediaExtensions[strings.ToLower(path.Ext(name))]
}
