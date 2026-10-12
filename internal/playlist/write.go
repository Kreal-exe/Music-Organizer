package playlist

import (
	"fmt"
	"net/url"
	"path"
	"strings"
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
		return iTunesXML(name, tracks, windows)
	}
	return []byte(b.String())
}

// iTunesXML writes a playlist the way Music and iTunes export one as XML
// (File → Library → Export Playlist), which both import on a Mac and on
// Windows. Their text export is no way back in: Music turns down a table
// that lacks any of the columns it writes itself — size, kind, dates — and
// those are not the playlist's to know.
func iTunesXML(name string, tracks []Track, windows bool) []byte {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n" +
		"<plist version=\"1.0\">\n<dict>\n" +
		"\t<key>Major Version</key><integer>1</integer>\n" +
		"\t<key>Minor Version</key><integer>1</integer>\n" +
		"\t<key>Application Version</key><string>1.0</string>\n" +
		"\t<key>Tracks</key>\n\t<dict>\n")
	for i, track := range tracks {
		fmt.Fprintf(&b, "\t\t<key>%d</key>\n\t\t<dict>\n\t\t\t<key>Track ID</key><integer>%d</integer>\n", i+1, i+1)
		fmt.Fprintf(&b, "\t\t\t<key>Name</key><string>%s</string>\n", escape(titleOf(track)))
		if track.Entry.Artist != "" {
			fmt.Fprintf(&b, "\t\t\t<key>Artist</key><string>%s</string>\n", escape(oneLine(track.Entry.Artist)))
		}
		if track.Entry.Duration > 0 {
			fmt.Fprintf(&b, "\t\t\t<key>Total Time</key><integer>%d</integer>\n", track.Entry.Duration*1000)
		}
		fmt.Fprintf(&b, "\t\t\t<key>Location</key><string>%s</string>\n\t\t</dict>\n", escape(FileURL(track.Path, windows)))
	}
	fmt.Fprintf(&b, "\t</dict>\n\t<key>Playlists</key>\n\t<array>\n\t\t<dict>\n\t\t\t<key>Name</key><string>%s</string>\n"+
		"\t\t\t<key>Playlist Items</key>\n\t\t\t<array>\n", escape(name))
	for i := range tracks {
		fmt.Fprintf(&b, "\t\t\t\t<dict><key>Track ID</key><integer>%d</integer></dict>\n", i+1)
	}
	b.WriteString("\t\t\t</array>\n\t\t</dict>\n\t</array>\n</dict>\n</plist>\n")
	return []byte(b.String())
}

// FileURL is a file's location as iTunes and Music write it:
// "file:///Users/me/Music/a%20b.mp3", or "file://localhost/C:/Music/a%20b.mp3"
// on Windows. The name is percent-encoded byte for byte, so it stays spelled
// exactly as the file is.
func FileURL(file string, windows bool) string {
	parts := strings.Split(strings.ReplaceAll(file, `\`, "/"), "/")
	for i, part := range parts {
		if windows && i == 0 && len(part) == 2 && part[1] == ':' {
			continue // The drive letter's colon stays as it is.
		}
		parts[i] = url.PathEscape(part)
	}
	joined := strings.Join(parts, "/")
	if windows {
		return "file://localhost/" + strings.TrimPrefix(joined, "/")
	}
	return "file://" + joined
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
