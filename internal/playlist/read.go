package playlist

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// Read reads a playlist file on this computer.
func Read(file string) ([]Playlist, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return Parse(data, filepath.Base(file), filepath.ToSlash(filepath.Dir(file))), nil
}

// Parse reads a playlist of any known format. An iTunes library export (.xml)
// gives every playlist in it; a .txt that is not an iTunes export, or a file
// in no known format, gives nothing.
func Parse(data []byte, fileName, folder string) []Playlist {
	ext := strings.ToLower(path.Ext(fileName))
	name := strings.TrimSuffix(fileName, path.Ext(fileName))

	var entries []Entry
	format := strings.ToUpper(strings.TrimPrefix(ext, "."))
	switch ext {
	case ".xml":
		playlists := readITunesXML(data)
		for i := range playlists {
			playlists[i].Folder = folder
		}
		return playlists
	case ".xspf":
		_, entries = readXSPF(data)
	case ".wpl", ".zpl":
		entries = readWPL(data)
	case ".txt":
		entries = readITunesText(decodeText(data))
		format = "iTunes"
	case ".pls":
		entries = readPLS(decodeText(data))
	case ".m3u", ".m3u8":
		entries = readM3U(decodeText(data))
	}
	if entries == nil {
		return nil
	}
	return []Playlist{{Name: name, Format: format, Entries: entries, Folder: folder}}
}

// decodeText reads UTF-16 or UTF-8, with or without a byte order mark, and
// otherwise — an old .m3u written by a Windows player — the Windows code page
// whose letters the bytes most look like.
func decodeText(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return utf16Text(data[2:], false)
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return utf16Text(data[2:], true)
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return string(data[3:])
	}
	// UTF-16 without a mark: every other byte of Latin text is zero.
	if len(data) >= 4 && len(data)%2 == 0 {
		if data[0] != 0 && data[1] == 0 && data[2] != 0 && data[3] == 0 {
			return utf16Text(data, false)
		}
		if data[0] == 0 && data[1] != 0 && data[2] == 0 && data[3] != 0 {
			return utf16Text(data, true)
		}
	}
	if utf8.Valid(data) {
		return string(data)
	}
	return legacyText(data)
}

func utf16Text(data []byte, bigEndian bool) string {
	units := make([]uint16, len(data)/2)
	for i := range units {
		if bigEndian {
			units[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
		} else {
			units[i] = uint16(data[2*i+1])<<8 | uint16(data[2*i])
		}
	}
	return string(utf16.Decode(units))
}

// legacyText decodes bytes that are not UTF-8. Cyrillic in windows-1251 puts
// nearly every letter in 0xC0–0xFF; Western text in windows-1252 uses that
// range only for the odd accented letter between plain ASCII ones.
func legacyText(data []byte) string {
	high, upper := 0, 0
	for _, b := range data {
		if b >= 0x80 {
			high++
			if b >= 0xC0 {
				upper++
			}
		}
	}
	table := charmap.Windows1252
	if high > 0 && upper*10 >= high*8 && high*4 >= countLetters(data) {
		table = charmap.Windows1251
	}
	text, err := table.NewDecoder().Bytes(data)
	if err != nil {
		return string(data)
	}
	return string(text)
}

func countLetters(data []byte) int {
	n := 0
	for _, b := range data {
		if b >= 0x80 || (b|0x20 >= 'a' && b|0x20 <= 'z') {
			n++
		}
	}
	return n
}

// lines splits text into its non-blank lines, whatever ends them.
func lines(text string) []string {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// setTitle reads "Artist - Title" into an entry.
func setTitle(entry *Entry, text string) {
	text = strings.TrimSpace(text)
	if artist, title, found := strings.Cut(text, " - "); found {
		entry.Artist, entry.Title = artist, title
	} else {
		entry.Title = text
	}
}

func readM3U(text string) []Entry {
	entries := []Entry{}
	info, hasInfo := "", false
	for _, line := range lines(text) {
		if strings.HasPrefix(line, "#") {
			if strings.HasPrefix(strings.ToUpper(line), "#EXTINF:") {
				info, hasInfo = line[len("#EXTINF:"):], true
			}
			continue
		}
		entry := Entry{Location: line, Duration: -1}
		if hasInfo {
			// #EXTINF:215,Artist - Title — attributes may stand between the
			// length and the comma.
			length, title, found := strings.Cut(info, ",")
			if seconds := leadingNumber(length); seconds > 0 {
				entry.Duration = seconds
			}
			if found {
				setTitle(&entry, title)
			}
			hasInfo = false
		}
		entries = append(entries, entry)
	}
	return entries
}

func leadingNumber(text string) int {
	text = strings.TrimSpace(text)
	end := 0
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(text[:end])
	return n
}

var plsLine = regexp.MustCompile(`(?i)^\s*(File|Title|Length)(\d+)\s*=(.*)$`)

func readPLS(text string) []Entry {
	type item struct {
		entry    Entry
		hasFile  bool
		title    string
		duration int
	}
	items := map[int]*item{}
	for _, line := range lines(text) {
		match := plsLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		n, _ := strconv.Atoi(match[2])
		it := items[n]
		if it == nil {
			it = &item{duration: -1}
			items[n] = it
		}
		switch strings.ToLower(match[1]) {
		case "file":
			it.entry.Location, it.hasFile = match[3], true
		case "title":
			it.title = match[3]
		default:
			if seconds := leadingNumber(match[3]); seconds > 0 {
				it.duration = seconds
			}
		}
	}
	numbers := make([]int, 0, len(items))
	for n, it := range items {
		if it.hasFile {
			numbers = append(numbers, n)
		}
	}
	sort.Ints(numbers)
	entries := []Entry{}
	for _, n := range numbers {
		it := items[n]
		it.entry.Duration = it.duration
		if it.title != "" {
			setTitle(&it.entry, it.title)
		}
		entries = append(entries, it.entry)
	}
	return entries
}

// xmlNode is just enough of an XML tree for the playlist formats: they are
// small, and read by local names whatever namespace they declare.
type xmlNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Text     string     `xml:",chardata"`
	Children []xmlNode  `xml:",any"`
}

func parseXML(data []byte) (xmlNode, bool) {
	var root xmlNode
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	if err := decoder.Decode(&root); err != nil {
		return xmlNode{}, false
	}
	return root, true
}

func (n xmlNode) all(name string, into *[]xmlNode) {
	for _, child := range n.Children {
		if strings.EqualFold(child.XMLName.Local, name) {
			*into = append(*into, child)
		}
		child.all(name, into)
	}
}

func (n xmlNode) child(name string) (xmlNode, bool) {
	for _, child := range n.Children {
		if strings.EqualFold(child.XMLName.Local, name) {
			return child, true
		}
	}
	return xmlNode{}, false
}

func (n xmlNode) text(name string) string {
	child, _ := n.child(name)
	return strings.TrimSpace(child.Text)
}

func (n xmlNode) attr(name string) string {
	for _, attr := range n.Attrs {
		if strings.EqualFold(attr.Name.Local, name) {
			return attr.Value
		}
	}
	return ""
}

func readXSPF(data []byte) (string, []Entry) {
	root, ok := parseXML(data)
	if !ok {
		return "", nil
	}
	var tracks []xmlNode
	root.all("track", &tracks)
	entries := []Entry{}
	for _, track := range tracks {
		location := track.text("location")
		if location == "" {
			continue
		}
		// Relative locations are URIs too: "Artist/My%20Song.mp3".
		if !hasFileScheme(location) {
			if decoded, err := url.PathUnescape(location); err == nil {
				location = decoded
			}
		}
		entry := Entry{Location: location, Title: track.text("title"), Artist: track.text("creator"), Duration: -1}
		if ms := leadingNumber(track.text("duration")); ms > 0 {
			entry.Duration = ms / 1000
		}
		entries = append(entries, entry)
	}
	return root.text("title"), entries
}

func readWPL(data []byte) []Entry {
	root, ok := parseXML(data)
	if !ok {
		return nil
	}
	var media []xmlNode
	root.all("media", &media)
	entries := []Entry{}
	for _, item := range media {
		if source := item.attr("src"); source != "" {
			entries = append(entries, Entry{Location: source, Duration: -1})
		}
	}
	return entries
}

// readITunesText reads the "Unicode text" export of iTunes and Music. Its
// column titles are in the language iTunes runs in, but the order is fixed:
// the name first, the artist second, the location last.
func readITunesText(text string) []Entry {
	all := lines(text)
	if len(all) == 0 || len(strings.Split(all[0], "\t")) < 6 {
		return nil
	}
	header := strings.Split(all[0], "\t")
	timeColumn := -1
	for i, title := range header {
		switch strings.TrimSpace(title) {
		case "Time", "Время", "Dauer", "Durée", "Duración", "Durata", "Duur", "Tid", "Czas", "Duração", "Čas":
			timeColumn = i
		}
	}
	entries := []Entry{}
	for _, line := range all[1:] {
		columns := strings.Split(line, "\t")
		location := columns[len(columns)-1]
		if location == "" || len(columns) < 2 {
			continue
		}
		entry := Entry{Location: location, Title: columns[0], Artist: columns[1], Duration: -1}
		if timeColumn >= 0 && timeColumn < len(columns) {
			if seconds := leadingNumber(columns[timeColumn]); seconds > 0 {
				entry.Duration = seconds
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// readITunesXML reads the library export of iTunes or Music (File → Library →
// Export Library, or Export Playlist as XML): every playlist the user made,
// not the built-in ones.
func readITunesXML(data []byte) []Playlist {
	root, ok := parseXML(data)
	if !ok || !strings.EqualFold(root.XMLName.Local, "plist") {
		return nil
	}
	library, ok := root.child("dict")
	if !ok {
		return nil
	}
	top := plistDict(library)
	tracks := map[string]map[string]xmlNode{}
	if node, ok := top["Tracks"]; ok {
		for id, track := range plistDict(node) {
			tracks[id] = plistDict(track)
		}
	}
	playlistsNode, ok := top["Playlists"]
	if !ok {
		return nil
	}
	var playlists []Playlist
	for _, item := range playlistsNode.Children {
		info := plistDict(item)
		if plistTrue(info["Master"]) || plistTrue(info["Folder"]) || info["Distinguished Kind"].XMLName.Local != "" {
			continue
		}
		if visible, ok := info["Visible"]; ok && !plistTrue(visible) {
			continue
		}
		entries := []Entry{}
		for _, member := range info["Playlist Items"].Children {
			track := tracks[strings.TrimSpace(plistDict(member)["Track ID"].Text)]
			location := strings.TrimSpace(track["Location"].Text)
			if location == "" {
				continue
			}
			entry := Entry{Location: location, Title: track["Name"].Text, Artist: track["Artist"].Text, Duration: -1}
			if ms := leadingNumber(track["Total Time"].Text); ms > 0 {
				entry.Duration = ms / 1000
			}
			entries = append(entries, entry)
		}
		if len(entries) == 0 {
			continue
		}
		name := info["Name"].Text
		if name == "" {
			name = "Playlist"
		}
		playlists = append(playlists, Playlist{Name: name, Format: "iTunes XML", Entries: entries})
	}
	return playlists
}

// plistDict reads a <dict> into its values by key.
func plistDict(node xmlNode) map[string]xmlNode {
	values := map[string]xmlNode{}
	for i := 0; i+1 < len(node.Children); i++ {
		if node.Children[i].XMLName.Local == "key" {
			values[node.Children[i].Text] = node.Children[i+1]
			i++
		}
	}
	return values
}

func plistTrue(node xmlNode) bool {
	return node.XMLName.Local == "true"
}
