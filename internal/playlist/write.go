package playlist

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
)

// Relative is the path of file seen from folder ("../Other/a.mp3"), both
// absolute with forward slashes.
func Relative(file, folder string) string {
	target := strings.FieldsFunc(file, func(r rune) bool { return r == '/' })
	base := strings.FieldsFunc(folder, func(r rune) bool { return r == '/' })
	common := 0
	for common < len(target)-1 && common < len(base) && key(target[common]) == key(base[common]) {
		common++
	}
	parts := make([]string, 0, len(base)-common+len(target)-common)
	for range base[common:] {
		parts = append(parts, "..")
	}
	parts = append(parts, target[common:]...)
	return strings.Join(parts, "/")
}

// Write renders a playlist for target. Paths are written relative to folder,
// where the playlist will lie, except for iTunes, which wants them absolute.
// windows says the playlist is for a Windows computer, where the formats that
// allow either separator get backslashes.
func Write(target Target, name string, tracks []Track, folder string, windows bool) []byte {
	var b strings.Builder
	relative := func(track Track) string {
		rel := Relative(track.Path, folder)
		if windows {
			rel = strings.ReplaceAll(rel, "/", `\`)
		}
		return rel
	}

	switch target {
	case Android:
		// Nothing but the paths: what Android's media library reads, and the
		// way the players that read the file themselves expect it.
		b.WriteString("#EXTM3U\n")
		for _, track := range tracks {
			b.WriteString(Relative(track.Path, folder) + "\n")
		}

	case M3U8:
		b.WriteString("#EXTM3U\n")
		for _, track := range tracks {
			label := titleOf(track)
			if track.Entry.Artist != "" {
				label = oneLine(track.Entry.Artist) + " - " + label
			}
			fmt.Fprintf(&b, "#EXTINF:%d,%s\n%s\n", duration(track), label, relative(track))
		}

	case PLS:
		b.WriteString("[playlist]\n")
		for i, track := range tracks {
			fmt.Fprintf(&b, "File%d=%s\nTitle%d=%s\nLength%d=%d\n", i+1, relative(track), i+1, titleOf(track), i+1, duration(track))
		}
		fmt.Fprintf(&b, "NumberOfEntries=%d\nVersion=2\n", len(tracks))

	case XSPF:
		fmt.Fprintf(&b, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<playlist version=\"1\" xmlns=\"http://xspf.org/ns/0/\">\n  <title>%s</title>\n  <trackList>\n", escape(name))
		for _, track := range tracks {
			fmt.Fprintf(&b, "    <track>\n      <location>%s</location>\n      <title>%s</title>\n", escape(uriPath(Relative(track.Path, folder))), escape(titleOf(track)))
			if track.Entry.Artist != "" {
				fmt.Fprintf(&b, "      <creator>%s</creator>\n", escape(track.Entry.Artist))
			}
			if track.Entry.Duration > 0 {
				fmt.Fprintf(&b, "      <duration>%d</duration>\n", track.Entry.Duration*1000)
			}
			b.WriteString("    </track>\n")
		}
		b.WriteString("  </trackList>\n</playlist>\n")

	case WPL:
		fmt.Fprintf(&b, "<?wpl version=\"1.0\"?>\n<smil>\n    <head>\n        <meta name=\"Generator\" content=\"Music Library Organizer\"/>\n"+
			"        <meta name=\"ItemCount\" content=\"%d\"/>\n        <title>%s</title>\n    </head>\n    <body>\n        <seq>\n", len(tracks), escape(name))
		for _, track := range tracks {
			fmt.Fprintf(&b, "            <media src=\"%s\"/>\n", escape(strings.ReplaceAll(Relative(track.Path, folder), "/", `\`)))
		}
		b.WriteString("        </seq>\n    </body>\n</smil>\n")

	case ITunes:
		return iTunesText(tracks, windows)
	}
	return []byte(b.String())
}

// iTunesHeader is the row iTunes and Music export, column for column.
var iTunesHeader = []string{"Name", "Artist", "Composer", "Album", "Grouping", "Work", "Movement Number", "Movement Count",
	"Movement Name", "Genre", "Size", "Time", "Disc Number", "Disc Count", "Track Number", "Track Count", "Year",
	"Date Modified", "Date Added", "Bit Rate", "Sample Rate", "Volume Adjustment", "Kind", "Equalizer", "Comments",
	"Plays", "Last Played", "Skips", "Last Skipped", "My Rating", "Location"}

// iTunesText writes the table iTunes and Music export and import: UTF-16 with
// a byte order mark, a line per track, carriage returns between lines.
func iTunesText(tracks []Track, windows bool) []byte {
	var rows []string
	rows = append(rows, strings.Join(iTunesHeader, "\t"))
	for _, track := range tracks {
		columns := make([]string, len(iTunesHeader))
		columns[0] = titleOf(track)
		columns[1] = oneLine(track.Entry.Artist)
		if track.Entry.Duration > 0 {
			columns[11] = fmt.Sprint(track.Entry.Duration)
		}
		columns[len(columns)-1] = ITunesLocation(track.Path, windows)
		rows = append(rows, strings.Join(columns, "\t"))
	}
	units := utf16.Encode([]rune(strings.Join(rows, "\r") + "\r"))
	out := make([]byte, 2, 2+2*len(units))
	out[0], out[1] = 0xFF, 0xFE
	for _, unit := range units {
		out = append(out, byte(unit), byte(unit>>8))
	}
	return out
}

// ITunesLocation is a path the way iTunes writes it: "C:\Users\me\Music\a.mp3"
// on Windows, and on a Mac the volume's name followed by the path on that
// volume — "Macintosh HD/Users/me/Music/a.mp3".
func ITunesLocation(file string, windows bool) string {
	if windows {
		return strings.ReplaceAll(file, "/", `\`)
	}
	if rest, found := strings.CutPrefix(file, "/Volumes/"); found {
		return rest
	}
	return bootVolumeName() + file
}

// bootVolumeName finds the startup disk's name: /Volumes holds a link to "/"
// under that name.
func bootVolumeName() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	entries, err := os.ReadDir("/Volumes")
	if err != nil {
		return "Macintosh HD"
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		if target, err := filepath.EvalSymlinks(path.Join("/Volumes", entry.Name())); err == nil && target == "/" {
			return entry.Name()
		}
	}
	return "Macintosh HD"
}

func titleOf(track Track) string {
	if track.Entry.Title != "" {
		return oneLine(track.Entry.Title)
	}
	base := path.Base(track.Path)
	return oneLine(strings.TrimSuffix(base, path.Ext(base)))
}

func duration(track Track) int {
	if track.Entry.Duration > 0 {
		return track.Entry.Duration
	}
	return -1
}

// oneLine keeps tabs and line breaks out of a line-based format.
func oneLine(text string) string {
	return strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(text)
}

func escape(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(text)
}

// uriPath percent-encodes each part of a relative path for a URI.
func uriPath(rel string) string {
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// SafeName makes a playlist's name into a file name every system accepts.
func SafeName(name string) string {
	safe := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '-'
		}
		return r
	}, name)
	safe = strings.TrimSpace(strings.TrimRight(safe, ". "))
	if safe == "" {
		return "Playlist"
	}
	return safe
}
