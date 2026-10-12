package device

import (
	"context"
	"strings"
	"testing"
)

// The music on a phone is listed with each name exactly as the phone has it,
// a decomposed "й" included, and without hidden folders or other files.
func TestMediaFilesKeepSpelling(t *testing.T) {
	decomposed := "Кишлак/Танцуй (intro) .mp3"
	adb := &fakeADB{replies: map[string]string{
		"find '/sdcard/Music/' -type f": "/sdcard/Music/" + decomposed + "\r\n" +
			"/sdcard/Music/Juice/Lucid Dreams.m4a\n" +
			"/sdcard/Music/Juice/cover.jpg\n" +
			"/sdcard/Music/.old/a.mp3\n" +
			"/sdcard/Music/Favourites.m3u\n",
	}}
	phone := &Phone{adb: adb, Serial: "X"}

	files, err := phone.MediaFiles(context.Background(), "/sdcard/Music/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{decomposed, "Juice/Lucid Dreams.m4a"}
	if strings.Join(files, "|") != strings.Join(want, "|") {
		t.Errorf("files %q, want %q", files, want)
	}
}

// Each playlist file comes with how many of its tracks the media database
// matched, and files the database does not know say so.
func TestPlaylistsCountWhatThePhoneShows(t *testing.T) {
	adb := &fakeADB{replies: map[string]string{
		"-iname '*.m3u'": "/storage/emulated/0/Music/Favourites.m3u\n" +
			"/storage/emulated/0/Music/New.m3u8\n" +
			"/storage/emulated/0/Android/data/app/cache.m3u\n" +
			"/storage/emulated/0/Download/.trashed-1-Old.m3u\n",
		"--projection _id:_data": "Row: 0 _id=35481, _data=/storage/emulated/0/Music/Favourites.m3u\n",
		"for i in":               "35481 468\n",
	}}
	phone := &Phone{adb: adb, Serial: "X"}

	files, err := phone.Playlists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files %+v", files)
	}
	if files[0].Path != "/storage/emulated/0/Music/Favourites.m3u" || files[0].Visible != 468 {
		t.Errorf("first %+v", files[0])
	}
	if files[1].Visible != -1 {
		t.Errorf("a file the database does not list: %+v", files[1])
	}
}

// A playlist written over a different one keeps the old file as .bak; the
// same playlist written again leaves nothing behind.
func TestWritePlaylist(t *testing.T) {
	for _, tc := range []struct {
		old    string
		backup bool
	}{{"#EXTM3U\nold.mp3\n", true}, {"#EXTM3U\na.mp3\nb.mp3\n", false}, {"", false}} {
		adb := &fakeADB{replies: map[string]string{
			"cat '/sdcard/Music/Mix.m3u'": tc.old,
			"--projection _id:_data":      "Row: 0 _id=7, _data=/storage/emulated/0/Music/Mix.m3u\n",
			"playlists/7/members":         "Row: 0 audio_id=1\nRow: 1 audio_id=2\n",
		}}
		phone := &Phone{adb: adb, Serial: "X"}

		data := []byte("#EXTM3U\na.mp3\nb.mp3\n")
		visible, err := phone.WritePlaylist(context.Background(), "/sdcard/Music/Mix.m3u", data, 2)
		if err != nil {
			t.Fatal(err)
		}
		if visible != 2 {
			t.Errorf("visible %d, want 2", visible)
		}
		if adb.pushed["/sdcard/Music/Mix.m3u"] != string(data) {
			t.Errorf("pushed %q", adb.pushed)
		}
		backedUp := false
		for _, call := range adb.calls {
			backedUp = backedUp || strings.Contains(call, "cp -f '/sdcard/Music/Mix.m3u' '/sdcard/Music/Mix.m3u.bak'")
		}
		if backedUp != tc.backup {
			t.Errorf("old %q: backed up %v, want %v", tc.old, backedUp, tc.backup)
		}
	}
}
