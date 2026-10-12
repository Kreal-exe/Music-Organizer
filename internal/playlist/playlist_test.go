package playlist

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/unicode/norm"
)

// A name as macOS often stores it, with "й" as "и" and a combining breve: a
// playlist that spells it the usual way names a different file to Android.
const decomposed = "Кишлак/Танцуи\u0306.mp3"

func musicFolder(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{decomposed, "707/Intro.mp3", "Other/Intro.mp3", "Juice/Lucid Dreams.m4a", "Juice/notes.txt", ".hidden/x.mp3"} {
		file := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// iTunesHeader is the row iTunes and Music put at the top of their text
// export, column for column.
var iTunesHeader = []string{"Name", "Artist", "Composer", "Album", "Grouping", "Work", "Movement Number", "Movement Count",
	"Movement Name", "Genre", "Size", "Time", "Disc Number", "Disc Count", "Track Number", "Track Count", "Year",
	"Date Modified", "Date Added", "Bit Rate", "Sample Rate", "Volume Adjustment", "Kind", "Equalizer", "Comments",
	"Plays", "Last Played", "Skips", "Last Skipped", "My Rating", "Location"}

func utf16Export(rows ...[]string) []byte {
	var lines []string
	lines = append(lines, strings.Join(iTunesHeader, "\t"))
	for _, row := range rows {
		columns := make([]string, len(iTunesHeader))
		columns[0], columns[1], columns[11], columns[30] = row[0], "Кишлак", row[1], row[2]
		lines = append(lines, strings.Join(columns, "\t"))
	}
	units := utf16.Encode([]rune(strings.Join(lines, "\r") + "\r"))
	out := []byte{0xFF, 0xFE}
	for _, unit := range units {
		out = append(out, byte(unit), byte(unit>>8))
	}
	return out
}

func paths(tracks []Track) []string {
	out := make([]string, len(tracks))
	for i, track := range tracks {
		out[i] = track.Path
	}
	return out
}

// An iTunes export made under another user, on another volume and on Windows
// finds its tracks by the tail of their paths, and the Android playlist spells
// every name exactly as it is on the disk.
func TestITunesExportToAndroid(t *testing.T) {
	root := musicFolder(t)
	index, err := IndexFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	if index.Len() != 4 {
		t.Fatalf("indexed %d files, want 4 (no text files, nothing hidden)", index.Len())
	}

	export := utf16Export(
		[]string{"Танцуй", "127", "OtherDisk/Users/someone/Music/Кишлак/Танцуй.mp3"},
		[]string{"Lucid", "240", `C:\Users\me\Music\Juice\Lucid Dreams.m4a`},
		[]string{"Intro", "", "OtherDisk/Users/someone/Music/707/Intro.mp3"},
		[]string{"Gone", "", "OtherDisk/Users/someone/Music/Gone/Nowhere.mp3"},
	)
	lists := Parse(export, "Lieblings.txt", "/nowhere")
	if len(lists) != 1 || len(lists[0].Entries) != 4 || lists[0].Format != "iTunes" {
		t.Fatalf("read %+v", lists)
	}
	if first := lists[0].Entries[0]; first.Duration != 127 || first.Artist != "Кишлак" || first.Title != "Танцуй" {
		t.Errorf("first entry %+v", first)
	}

	found, missing := index.Find(lists[0])
	// "707/Intro.mp3" is told apart from "Other/Intro.mp3" by its folder.
	if len(found) != 3 || len(missing) != 1 || missing[0].Title != "Gone" {
		t.Fatalf("found %v, missing %v", paths(found), missing)
	}

	folder := index.Root
	got := string(Write(Android, "Favourites", found, folder, false))
	want := "#EXTM3U\n" + decomposed + "\nJuice/Lucid Dreams.m4a\n707/Intro.mp3\n"
	if got != want {
		t.Errorf("Android playlist:\n%q\nwant\n%q", got, want)
	}
	if got == norm.NFC.String(want) {
		t.Error("the name was rewritten in the composed form")
	}
}

func TestAmbiguousNames(t *testing.T) {
	index, err := IndexFolder(musicFolder(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := index.Resolve("Intro.mp3", "/nowhere"); ok {
		t.Error("a file name two files share was matched to one of them")
	}
	if _, ok := index.Resolve("lucid dreams.M4A", "/nowhere"); !ok {
		t.Error("letter case kept a unique name from being found")
	}
	if _, ok := index.Resolve("Танцуй.mp3", "/nowhere"); !ok {
		t.Error("the composed spelling did not find the decomposed file")
	}
}

// Every format written reads back with the same tracks, titles and lengths.
func TestRoundTrip(t *testing.T) {
	root := musicFolder(t)
	index, _ := IndexFolder(root)
	source := Playlist{Name: "Mix", Folder: "/nowhere", Entries: []Entry{
		{Location: "/x/Кишлак/Танцуй.mp3", Title: "Танцуй", Artist: "Кишлак", Duration: 127},
		{Location: "Juice/Lucid Dreams.m4a", Duration: -1},
		{Location: "file:///y/707/Intro.mp3", Title: "Intro & Outro", Duration: -1},
	}}
	tracks, missing := index.Find(source)
	if len(tracks) != 3 || len(missing) != 0 {
		t.Fatalf("found %v, missing %v", paths(tracks), missing)
	}

	out := filepath.ToSlash(filepath.Join(filepath.Dir(index.Root), "playlists"))
	for _, info := range Targets {
		for _, windows := range []bool{false, true} {
			data := Write(info.ID, "Mix & Co", tracks, out, windows)
			back := Parse(data, "Mix."+info.Extension, out)
			if len(back) != 1 {
				t.Fatalf("%s: read back %d playlists", info.ID, len(back))
			}
			if info.ID == ITunes && windows {
				continue // Windows paths only resolve on Windows.
			}
			again, _ := index.Find(back[0])
			if !reflect.DeepEqual(paths(again), paths(tracks)) {
				t.Errorf("%s (windows %v): %v, want %v", info.ID, windows, paths(again), paths(tracks))
			}
			if info.ID != Android && info.ID != WPL {
				if first := back[0].Entries[0]; first.Title != "Танцуй" || first.Duration != 127 {
					t.Errorf("%s: first entry %+v", info.ID, first)
				}
			}
		}
	}

	m3u8 := string(Write(M3U8, "Mix", tracks, out, false))
	if !strings.Contains(m3u8, "#EXTINF:127,Кишлак - Танцуй\n../") {
		t.Errorf("m3u8:\n%s", m3u8)
	}
	if wpl := string(Write(WPL, "Mix", tracks, out, false)); !strings.Contains(wpl, `src="..\`) {
		t.Errorf("wpl:\n%s", wpl)
	}
}

// A playlist for a phone is resolved against the phone's own list of files,
// whatever the playlist wrote.
func TestPhoneIndex(t *testing.T) {
	phone := NewIndex("/storage/emulated/0/Music", []string{decomposed, "Juice/Lucid Dreams.m4a"}, false)
	list := Parse([]byte("#EXTM3U\n#EXTINF:12,A - B\n/sdcard/Music/Juice/Lucid Dreams.m4a\nКишлак/Танцуй.mp3\n"), "x.m3u", "/storage/emulated/0/Music")
	if len(list) != 1 {
		t.Fatal("not read")
	}
	if entry := list[0].Entries[0]; entry.Artist != "A" || entry.Title != "B" || entry.Duration != 12 {
		t.Errorf("entry %+v", entry)
	}
	found, missing := phone.Find(list[0])
	if len(found) != 2 || len(missing) != 0 || found[1].Path != "/storage/emulated/0/Music/"+decomposed {
		t.Errorf("found %v, missing %v", paths(found), missing)
	}
}

func TestLegacyEncodings(t *testing.T) {
	cyrillic, _ := charmap.Windows1251.NewEncoder().String("#EXTM3U\nМузыка/Кино - Группа крови.mp3\n")
	if list := Parse([]byte(cyrillic), "old.m3u", "/"); list[0].Entries[0].Location != "Музыка/Кино - Группа крови.mp3" {
		t.Errorf("windows-1251: %q", list[0].Entries[0].Location)
	}
	western, _ := charmap.Windows1252.NewEncoder().String("#EXTM3U\nMusic/Beyoncé - Halo.mp3\n")
	if list := Parse([]byte(western), "old.m3u", "/"); list[0].Entries[0].Location != "Music/Beyoncé - Halo.mp3" {
		t.Errorf("windows-1252: %q", list[0].Entries[0].Location)
	}
}

func TestITunesXML(t *testing.T) {
	library := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Tracks</key><dict>
    <key>101</key><dict><key>Track ID</key><integer>101</integer><key>Name</key><string>Lucid Dreams</string>
      <key>Artist</key><string>Juice WRLD</string><key>Total Time</key><integer>239000</integer>
      <key>Location</key><string>file:///Users/me/Music/Juice/Lucid%20Dreams.m4a</string></dict>
  </dict>
  <key>Playlists</key><array>
    <dict><key>Name</key><string>Library</string><key>Master</key><true/>
      <key>Playlist Items</key><array><dict><key>Track ID</key><integer>101</integer></dict></array></dict>
    <dict><key>Name</key><string>Music</string><key>Distinguished Kind</key><integer>4</integer>
      <key>Playlist Items</key><array><dict><key>Track ID</key><integer>101</integer></dict></array></dict>
    <dict><key>Name</key><string>Night &amp; Day</string>
      <key>Playlist Items</key><array><dict><key>Track ID</key><integer>101</integer></dict></array></dict>
  </array>
</dict></plist>`
	lists := Parse([]byte(library), "Library.xml", "/x")
	if len(lists) != 1 || lists[0].Name != "Night & Day" || len(lists[0].Entries) != 1 {
		t.Fatalf("read %+v", lists)
	}
	entry := lists[0].Entries[0]
	if entry.Duration != 239 || entry.Artist != "Juice WRLD" {
		t.Errorf("entry %+v", entry)
	}
	if got := pathFromLocation(entry.Location); got != "/Users/me/Music/Juice/Lucid Dreams.m4a" {
		t.Errorf("location %q", got)
	}
	if got := pathFromLocation("file://localhost/C:/Music/a%20b.mp3"); got != "C:/Music/a b.mp3" {
		t.Errorf("windows location %q", got)
	}
}

func TestNotAPlaylist(t *testing.T) {
	if lists := Parse([]byte("just some notes\nsecond line\n"), "notes.txt", "/"); lists != nil {
		t.Errorf("a text file read as %+v", lists)
	}
	if lists := Parse([]byte("<plist><dict><key>a</key><string>b</string></dict></plist>"), "Info.xml", "/"); lists != nil {
		t.Errorf("a property list read as %+v", lists)
	}
}

func TestRelativeAndNames(t *testing.T) {
	if got := Relative("/a/b/c.mp3", "/a/d"); got != "../b/c.mp3" {
		t.Errorf("Relative = %q", got)
	}
	if got := SafeName(`AC/DC: "Best"?`); got != "AC-DC- -Best--" {
		t.Errorf("SafeName = %q", got)
	}
}

func TestFileURL(t *testing.T) {
	if got := FileURL("/Users/me/Music/"+decomposed, false); got != "file:///Users/me/Music/%D0%9A%D0%B8%D1%88%D0%BB%D0%B0%D0%BA/%D0%A2%D0%B0%D0%BD%D1%86%D1%83%D0%B8%CC%86.mp3" {
		t.Errorf("mac: %s", got)
	}
	if got := FileURL("C:/Users/me/Music/a b.mp3", true); got != "file://localhost/C:/Users/me/Music/a%20b.mp3" {
		t.Errorf("windows: %s", got)
	}
	if got := pathFromLocation(FileURL("/x/"+decomposed, false)); got != "/x/"+decomposed {
		t.Errorf("round trip: %q", got)
	}
}
