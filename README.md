<div align="center">

<img src="build/appicon.png" width="120" alt="" />

# Music Library Organizer

**Bring a music collection's artist list back to the artists you actually listen to.**

Windows · macOS · works directly on an Android phone over adb

</div>

---

A player builds its artist list out of whatever is written in the tags, not out
of what you think you own. So a library of thirteen artists shows up as
fifty-eight:

| What is in the file | What the player shows |
|---|---|
| Album artist `Various Artists` | a whole compilation filed under an artist nobody listens to |
| The compilation flag (`TCMP` / `cpil` / `COMPILATION`) | the same, with no album artist involved |
| `Future, Juice Wrld, Young Thug` | a one-track artist that exists once |
| `Juice WRLD`, `juice wrld`, `JUICE WRLD` | three artists |
| `City Morgue␀Sosmula` (two values in one field) | one artist with a square in the middle of its name |

This tool reads the whole collection, shows the list as it would be, and lets
you fold it down to what you meant — then writes it back. Nothing is written
until you press **Apply**, and **Show changes** prints, file by file, what is
about to happen.

## What it does

**Artists.** Every name in the files, reduced to the artist it belongs to:
spellings of one name merged, guest credits detached from the lead, and any
artist you want put under another one by hand. The heading above the list is a
running total of what the ticks add up to, and beneath it every fold the tool
suggests, each with a tick. Nothing is ticked until you tick it: tick the
names that belong together, a whole artist at once or everything with *Tick
all*, or type a different artist into the heading and the ticked names go
there instead.

**Albums.** The same for albums, always within one artist — two artists' *Greatest
Hits* stay two albums. Spellings, word order, editions (*(Deluxe)*, *[Explicit]*,
*- Single*, *(Remastered 2011)*), a shared-by watermark and discs filed as albums
of their own (*(CD1)*, *(CD2)*) are offered as one album, and a disc keeps its
number as it rejoins. Tick what should merge, rename an album, or merge it into
another album by the same artist.

**Tracks.** The whole library in one sortable table, the way a desktop player
shows it. Select a few rows, or a few hundred, and set artist, album artist,
album, title, genre, year, track and disc number on all of them at once. Right
click for the file itself: show it in the file manager, copy its path, select
everything by the same artist, select the rest of the album.

**Lyrics.** Finds lyrics for the tracks that have none and writes them into the
files — `USLT` in MP3, `LYRICS` in FLAC, `©lyr` in MP4 — timed line by line
where a source has the timings, so a phone player scrolls them with the song.
A track no search can place is finished by pasting its page from a lyrics site.

**Cleanup.** The tags that invent artists: set the album artist from the artist
or remove it entirely, clear the compilation flag, clear the album artist sort
name, remove the obsolete tag at the end of a file.

**Artwork.** A cover saved at full resolution makes a tag of several megabytes,
and a phone's media scanner stops reading a tag at three — so the file's artist
and title never reach the player at all. Covers on those files are re-encoded
to fit within 1000 pixels, which is more than any screen shows them at: one
real file went from a 12.9 MB tag to 102 KB, and the phone started reading it.

**Playlists.** Moves playlists from one player to another: iTunes and Music
(their *Unicode text* and XML exports), Android, VLC, foobar2000, Windows Media
Player. Each track is looked up in the open library — by its path, or when the
playlist was made under another user, drive or computer, by the longest end of
its path that names exactly one file — and the playlist is written for the
player you pick, beside the music, in another folder or straight onto a phone.
The phone's own playlists are listed too, each with how many of its tracks the
phone actually shows.

## Playlists

| Player | Written as |
|---|---|
| Android — Phonograph, Poweramp, Samsung Music, Musicolet… | `.m3u`, UTF-8, paths from the playlist's folder |
| iTunes and Music, Mac and Windows | `.txt`, the table they export, UTF-16 with absolute paths — *File → Library → Import Playlist* |
| VLC, foobar2000, AIMP, Winamp, MusicBee | `.m3u8` with titles and lengths |
| Windows Media Player | `.wpl` |
| Strawberry, Clementine, VLC | `.xspf` |
| Winamp and older players | `.pls` |

All of these are read as well, along with an iTunes library exported as XML
(every playlist in it) and an old `.m3u` in a Windows code page.

A playlist is only useful if every path in it is spelled the way the file is
named, byte for byte, and that is where playlists moved by hand go wrong. macOS
often stores a name such as *Танцуй* with the *й* as two characters, a letter
and a combining mark; copied to a phone the name keeps that form, and a
playlist that spells it as one character names a file Android does not have —
it shows the playlist with those tracks left out and no error anywhere. So
names are matched regardless of Unicode form and letter case, but written
exactly as the files are named where the playlist is going: on this computer
as they are on its disk, on a phone as the phone lists them.

Saved onto a phone, a playlist is handed to the media library at once and
counted again until the count settles, and the result says how many tracks
the phone shows. A different playlist of the same name is kept beside the new
one as `.bak`, on the phone and on this computer alike.

## Formats

| Format | Tags |
|---|---|
| MP3 | ID3v2.2, ID3v2.3, ID3v2.4 — v2.2 and v2.3 are upgraded to v2.4 on write, artwork and all |
| FLAC | Vorbis comments |
| M4A / AAC / ALAC (`.m4a`, `.m4b`, `.m4p`, `.mp4`) | iTunes-style `ilst` atoms |

Files are replaced whole and atomically: the new file is written beside the old
one and swapped in, so a crash halfway through cannot leave a broken track.
The modification date is put back afterwards, so nothing re-sorts under
"recently added".

Two things in a real collection defeat a strict reader, and both are handled:

- **A tag that cannot be walked in one pass.** A large picture frame the usual
  parser stops reading half-way through leaves it looking at image data where
  the next frame header should be, and the file reads as unreadable although
  every field is still there. Those tags are read frame by frame instead, and
  written back in a form any parser can read before the file is edited.
- **The obsolete 128-byte tag at the end of a file.** Android's media database
  reads it in preference whenever the real tag is over about three megabytes —
  which one uncompressed cover exceeds on its own — so a file carrying both
  keeps showing the name it was given years ago. Every write brings that tag
  into line with the real one, or takes it off where nothing needs it. A name
  it cannot spell, such as a Cyrillic one, is never written into it in a
  mangled form: the tag goes instead.

## Working on a phone

An Android phone is not a disk. What Windows shows over MTP has no path a
program can open, and copying a 23 GB collection to the computer and back to
change a few hundred bytes per file would move all of it twice.

So the phone half runs on the phone. A small static agent travels inside the
application, is pushed to `/data/local/tmp` over adb, and reads and writes tags
in place; only the tags themselves cross the cable. The agent is identified by
the hash of its own binary, so a release that changes the tag reader always
replaces the agent that is already on the phone.

What you need:

1. **USB debugging** turned on in the phone's developer options.
2. **adb** — found automatically in `PATH`, in the Android SDK, in the winget
   install location, or in Homebrew. If it is not installed,
   [Platform Tools](https://developer.android.com/tools/releases/platform-tools)
   is the whole download.
3. The confirmation dialog on the phone, the first time.

If the dialog never appears, the computer is not seeing adb on the phone at
all — `adb devices` lists nothing, not even *unauthorized*. USB debugging has
been switched off (phones do it themselves after an update), or the phone is
connected for charging only. On Honor and Huawei also turn on *Allow ADB
debugging in charge only mode*; if it still does not ask, *Revoke USB
debugging authorisations*, switch debugging off and on, and plug in again.

Then pick **Android phone** instead of a folder, choose the music folder, and
work as usual. After applying, the tool asks Android to re-index the files it
changed, so players see the new tags without a reboot — and then to read the
playlists again.

That second step is not housekeeping. A playlist on Android is a list of
database row numbers, not of file names, so rewriting a track's tags gives it a
new row and every playlist that pointed at the old one silently loses the
entry. The playlist files on disk are never touched, so having the scanner read
them again puts every entry back; without it, a collection comes out of a tag
edit with its playlists gutted. The phone is given a moment to finish indexing
first, since a playlist read while its tracks are still being indexed comes
back short, and each one is checked against its file and read again if it
did.

This takes about a minute at the end of a job, and the status line says so.

Only 64-bit ARM phones are supported, which is every Android phone still
receiving applications.

## Lyrics sources

Sources are asked in turn, and the first confident match wins:

1. **[LRCLIB](https://lrclib.net)** — open, no key, no account. Good on
   released music, and it reports the track's length, which is what rules out
   a same-titled song by somebody else.
2. **[Genius](https://genius.com)** — picks up what LRCLIB has never heard of:
   leaks, mixtapes, unreleased tracks.
3. **[lyrics.ovh](https://lyrics.ovh)** — asked last and only about what the
   other two could not place. It cannot search, only answer about a name it is
   given, so it is never allowed to answer ahead of a source that looked.

A match is scored on artist and title, allowing for the junk tags collect
(`(Official Audio)`, `[Prod. Nick Mira]`, `feat.` tails) and for how close the
durations are. An unconvincing match is thrown away: no lyrics beats somebody
else's lyrics.

Tags are wrong in two ordinary ways, and each costs one more search rather
than a guess:

- **The artist field is not an artist.** A file from a download service is
  credited to the service — `VK`, `Various Artists`, `Unknown Artist` — and the
  real name survives only in the album artist, or in the file's own name, so
  `Artist - Title.mp3` is read and searched for as well. Names are also
  compared by the words they have in common, so a file credited to `ZillaKami
  x SosMula` still matches a database entry filed under `City Morgue`.
- **The title carries a qualifier no database knows.** `Deprived (Session
  Edit)`, `Devil Horns (v1)` and `Insecurities OG` return nothing anywhere;
  searched under the plain name they are all found, and another take of the
  same song is accepted as a match.
- **The title repeats the artist**, as a download names its file: `Lil Uzi
  Vert - No Script` is searched for under `No Script`, since asking for the
  artist twice finds nothing.
- **A collaboration is filed under the guest.** `Act Right (feat. GDo)` is
  GDo's song on Genius, so a candidate credited to a guest the title names
  counts as credited to somebody who was on the track. Guests are read in
  either alphabet, including the `п.у.` a Russian release writes.

What nearly matched is named instead of being thrown away silently: a track
the databases hold under an alias comes back as *not found (closest: …)*.

A source that answers 429 or 5xx is retried twice before it counts as failed,
and a source falling over is only reported when no source answered at all —
otherwise the track was searched for properly and simply is not there.

The counters under the progress bar — found, not found, errors, with timings
— are filters: click one to see only the tracks it counts, and again to see
them all. A track that was found but could not be written moves from *found*
to *errors*, with the reason.

### Lyrics from a link

Some tracks no search can reach: a Russian act catalogued under a
transliteration of its name, a leak credited to whoever leaked it. Every line
the search could not fill has a field for the song's page, and the same is in
the track menu as **Lyrics from a link…**. What came back is shown before
anything is staged, and it is written with the next **Apply**.

| Site | How it is read |
|---|---|
| [Genius](https://genius.com) | its lyrics containers, without the contributor header and adverts |
| [AZLyrics](https://www.azlyrics.com) | from the notice that opens the song, so the title above is left out |
| [Amalgama](https://www.amalgama-lab.com) | the original only, without the translation beside it |
| [Musixmatch](https://www.musixmatch.com), [Letras](https://www.letras.mus.br), [SongLyrics](https://www.songlyrics.com) and any other site | the words the page declares for search engines, or its longest run of lines, whichever is fuller |

Any text encoding is read, so an old Russian site in windows-1251 comes out
right. Lyrics.com cannot be read: its page is filled in by a script, and holds
no words until a browser runs it.

Where a source has the words timed line by line, those are what goes into the
tag. Phonograph decides what to show with

```kotlin
val all = listOfNotNull(embedded) + externalPrecise + externalVague
val activated: Int = all.indexOfFirst { it is LrcLyrics }
```

— the embedded lyrics come first in that list, but nothing is shown at all
unless something in it carries timings, which is why plain words sit behind a
menu. Timed words in the same tag are picked up by themselves, and scroll with
the song.

## Building

You need [Go](https://go.dev/dl/) 1.21+ and the Wails CLI:

```
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

The interface is plain HTML, CSS and JavaScript in `frontend/dist`, so there is
no npm, no Node and no bundler step.

```
make build          # rebuilds the phone agent, then the application
```

or, without make:

```powershell
.\build.ps1
```

Windows refuses to run scripts until told otherwise. Either allow your own once
with `Set-ExecutionPolicy -Scope CurrentUser RemoteSigned`, or run it with
`powershell -ExecutionPolicy Bypass -File .\build.ps1`.

Both do the same two things, and the order matters — the agent is embedded into
the application, so it is built first:

```
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
    -o internal/device/agent/mlm-agent-arm64 ./cmd/agent
wails build
```

**Windows** needs WebView2, which Windows 11 already has and Windows 10 takes
[from here](https://developer.microsoft.com/microsoft-edge/webview2/). The
result, `build/bin/Music Library Organizer.exe`, needs nothing else.

**macOS** needs the Xcode command line tools (`xcode-select --install`) and
`~/go/bin` on the `PATH`, where `go install` puts `wails`:

```
./build.sh          # build/bin/MusicLibraryOrganizer.app, Apple Silicon and Intel
./build.sh zip      # the same, signed and zipped for a release
```

`build.sh` installs the Wails CLI if it is missing and runs `make mac` or
`make mac-zip`, which work just as well on their own.

A `.app` can only be built on a Mac — Apple does not allow
cross-compiling to its own platform. The Go toolchain builds for macOS 13 or
newer.

For development, `wails dev`.

**Releases** build themselves. Set `productVersion` in `wails.json`, commit,
and push an annotated tag whose message is the release text:

```
git tag -a v1.0.4 -F notes.md && git push origin v1.0.4
```

GitHub Actions then builds the Windows `.exe` and the universal macOS zip and
publishes both on the release. A tag that disagrees with `wails.json` stops
the run before anything is built.

Keep the checkout out of iCloud Drive. It syncs `.git` too, and after a commit
it leaves empty copies of git's object folders — `10`, `2f`, `d0` and so on —
in the root of the project.

The icon is generated rather than drawn:
`go run ./tools/icon build` rewrites `build/appicon.png` and
`build/windows/icon.ico`.

## Tests

```
go test ./...
```

The tag tests build real files with **ffmpeg** and check the result with
**ffprobe**, including that an edited file still decodes without complaint;
without ffmpeg they skip themselves. Some lyrics tests go to the network —
`go test -short ./...` leaves those out.

## Worth knowing

- **Backups are off by default.** Turn them on under *Cleanup* the first time
  you edit a collection: every changed file gets a `.bak` copy beside it, which
  scanning then ignores.
- **MP3 tags are upgraded to ID3v2.4 on write.** Older versions only offer
  UTF-16 for anything but Latin-1, and the common writer for it mangles
  Cyrillic. v2.4 allows UTF-8, and every current player reads it — Phonograph,
  foobar2000, VLC, iTunes.
- **Guest credits found by a separator are not detached on their own.** `&`,
  `;`, `/` and commas turn up inside real names too, as in `Earth, Wind &
  Fire`, so those are shown ticked but visible rather than applied silently.
- **A folder can be opened from the command line**, which is also what happens
  when one is dropped onto the application:
  ```
  "Music Library Organizer.exe" "D:\Music"
  ```

## Licence

MIT. See [LICENSE](LICENSE).
