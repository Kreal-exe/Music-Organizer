package main

// App is the bridge between the interface and the work. Every long-running
// job — scanning, applying edits, fetching lyrics — reports through events so
// the window stays responsive, and every one of them can be cancelled.
//
// A collection lives either in a folder on this computer or on a phone. The
// only difference is where tags are read and written; everything in between —
// the artist list, the plan, the lyrics — is the same either way.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	wr "github.com/wailsapp/wails/v2/pkg/runtime"

	"musiclibraryorganizer/internal/device"
	"musiclibraryorganizer/internal/library"
	"musiclibraryorganizer/internal/lyrics"
	"musiclibraryorganizer/internal/plan"
	"musiclibraryorganizer/internal/tags"
	"musiclibraryorganizer/internal/wire"
)

// How often progress reaches the interface. Emitting an event per file floods
// the bridge on a large collection without telling the user any more.
const progressInterval = 80 * time.Millisecond

// How many files the preview sends over; the count comes separately.
const previewLimit = 400

// How many lyrics are written back in one go. Writing to a phone costs a round
// trip, so they are batched — but not so far that stopping a long run throws
// much away.
const lyricsBatch = 40

type App struct {
	ctx    context.Context
	finder *lyrics.Finder

	mu      sync.Mutex
	lib     *library.Library
	changes []plan.Change
	// manual holds the edits made to individual tracks in the table, by path.
	manual map[string]tags.Edit
	phone  *device.Phone // nil when the collection is a folder on this computer
	cancel context.CancelFunc

	// lists holds the playlists the Playlists view has read.
	lists playlists
}

func NewApp() *App {
	return &App{finder: lyrics.NewFinder()}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// A folder given on the command line opens straight away, which is what
	// happens when a folder is dropped onto the application.
	if len(os.Args) > 1 {
		if info, err := os.Stat(os.Args[1]); err == nil && info.IsDir() {
			folder := os.Args[1]
			go func() {
				// The window has to exist before it can be told anything.
				time.Sleep(200 * time.Millisecond)
				wr.EventsEmit(a.ctx, "open:folder", folder)
			}()
		}
	}
}

func (a *App) domReady(context.Context) { a.fitWindow() }

// fitWindow keeps the window inside the screen it opens on. The preferred size
// suits a large display; on a 1280×720 laptop it would open taller than the
// desktop, with the footer — where Apply lives — hidden behind the taskbar.
func (a *App) fitWindow() {
	screens, err := wr.ScreenGetAll(a.ctx)
	if err != nil || len(screens) == 0 {
		return
	}

	screen := screens[0]
	for _, candidate := range screens {
		if candidate.IsCurrent {
			screen = candidate
			break
		}
	}

	// Sizes are set in logical pixels, which is what Size reports; the
	// deprecated fields are the fallback for a frontend that leaves it empty.
	width, height := screen.Size.Width, screen.Size.Height
	if width <= 0 || height <= 0 {
		width, height = screen.Width, screen.Height
	}
	if width <= 0 || height <= 0 {
		return
	}

	// The margins leave room for the window frame and the taskbar.
	want := min(windowWidth, width-60)
	tall := min(windowHeight, height-100)
	if want >= windowWidth && tall >= windowHeight {
		return
	}

	wr.WindowSetSize(a.ctx, want, tall)
	wr.WindowCenter(a.ctx)
}

func (a *App) shutdown(context.Context) { a.Cancel() }

// finished is the last thing a job that wrote to a phone does.
//
// Writing to a track detaches it from every playlist it was in, because a
// playlist holds database row numbers and the track has just been given a new
// one. Having the playlist files read again puts the entries back, and it is
// done here — once, at the end — rather than after each batch, because the
// next batch would undo it anyway.
func (a *App) finished(ctx context.Context) {
	a.mu.Lock()
	phone := a.phone
	a.mu.Unlock()

	if phone != nil {
		phone.RescanPlaylists(ctx, a.root())
	}
}

// onPhone says whether the open collection lives on a phone.
func (a *App) onPhone() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phone != nil
}

// root is the folder the open library was scanned from.
func (a *App) root() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.lib == nil {
		return ""
	}
	return a.lib.Root
}

// begin replaces any running job with a fresh cancellable context, so starting
// a new one always stops the last.
func (a *App) begin() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cancel != nil {
		a.cancel()
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.cancel = cancel
	return ctx
}

// Cancel stops whatever is running.
func (a *App) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
}

// ChooseFolder opens the system folder picker and returns what was chosen, or
// an empty string if the dialog was dismissed.
func (a *App) ChooseFolder() (string, error) {
	folder, err := wr.OpenDirectoryDialog(a.ctx, wr.OpenDialogOptions{
		Title: "Choose your music folder",
	})
	if err != nil || folder == "" {
		return "", err
	}

	// A phone plugged in as a media device shows up in the file manager, but
	// those folders sit in no filesystem and cannot be opened by any ordinary
	// program. Saying so is more use than a scan that finds nothing.
	if info, statErr := os.Stat(folder); statErr != nil || !info.IsDir() {
		return "", errors.New("this folder cannot be opened directly — it exists only inside the file " +
			"manager. If the music is on a phone, choose Android phone instead")
	}
	return folder, nil
}

/* Phone ------------------------------------------------------------------- */

// PhoneStatus is what the interface shows on the phone tab.
type PhoneStatus struct {
	HasADB  bool            `json:"hasADB"`
	Devices []device.Device `json:"devices"`
	Hint    string          `json:"hint,omitempty"`
}

// Phones reports which phones are attached and whether we can talk to them.
func (a *App) Phones() PhoneStatus {
	adb, err := device.Find()
	if err != nil {
		return PhoneStatus{
			Devices: []device.Device{},
			Hint: "adb was not found. It is part of Google's Android Platform Tools. " +
				"Windows: winget install Google.PlatformTools. " +
				"macOS: brew install --cask android-platform-tools",
		}
	}

	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()

	devices, err := device.Devices(ctx, adb)
	if err != nil {
		return PhoneStatus{HasADB: true, Devices: []device.Device{}, Hint: err.Error()}
	}

	status := PhoneStatus{HasADB: true, Devices: devices}
	if len(devices) == 0 {
		status.Hint = "No phone found. Connect it by cable and turn on USB debugging " +
			"under Developer options"
	}
	return status
}

// PhoneFolders lists the subfolders of a folder on the phone, so the user can
// find their music without typing a path.
func (a *App) PhoneFolders(serial, dir string) ([]string, error) {
	phone, err := a.connect(serial)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()

	if !phone.HasFolder(ctx, dir) {
		return nil, fmt.Errorf("there is no folder %s on the phone", dir)
	}
	return phone.Folders(ctx, dir)
}

// connect prepares the phone, copying the agent over if it is not already the
// current one.
func (a *App) connect(serial string) (*device.Phone, error) {
	adb, err := device.Find()
	if err != nil {
		return nil, errors.New("adb was not found — install the Android Platform Tools")
	}

	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	defer cancel()

	return device.Connect(ctx, adb, serial)
}

/* Scanning ---------------------------------------------------------------- */

// ScanResult is what the interface renders once a collection has been read.
type ScanResult struct {
	Root      string            `json:"root"`
	Source    string            `json:"source"` // "folder" or "phone"
	Summary   library.Summary   `json:"summary"`
	Errors    []library.Failure `json:"errors"`
	Cancelled bool              `json:"cancelled"`
}

// Scan reads every supported file in a folder on this computer.
func (a *App) Scan(root string) (ScanResult, error) {
	if strings.TrimSpace(root) == "" {
		return ScanResult{}, errors.New("no folder chosen")
	}
	ctx := a.begin()

	emit := throttle(func(p library.Progress) {
		wr.EventsEmit(a.ctx, "scan:progress", p)
	})

	lib, err := library.Scan(ctx, root, emit)
	cancelled := errors.Is(err, context.Canceled)
	if err != nil && !cancelled {
		return ScanResult{}, err
	}

	a.adopt(lib, nil)
	return ScanResult{
		Root:      root,
		Source:    "folder",
		Summary:   lib.Summarize(),
		Errors:    lib.Errors,
		Cancelled: cancelled,
	}, nil
}

// ScanPhone reads a folder on the phone. Tags are read by the agent running on
// the phone itself, so none of the audio is copied anywhere.
func (a *App) ScanPhone(serial, dir string) (ScanResult, error) {
	if strings.TrimSpace(dir) == "" {
		return ScanResult{}, errors.New("no folder given on the phone")
	}

	phone, err := a.connect(serial)
	if err != nil {
		return ScanResult{}, err
	}
	ctx := a.begin()

	emit := throttle(func(p device.Progress) {
		wr.EventsEmit(a.ctx, "scan:progress", library.Progress{Done: p.Done, Total: p.Total})
	})

	result, err := phone.Scan(ctx, dir, emit)
	cancelled := errors.Is(err, context.Canceled)
	if err != nil && !cancelled {
		return ScanResult{}, err
	}
	if result == nil {
		return ScanResult{}, errors.New("the phone sent nothing back")
	}

	failures := make([]library.Failure, 0, len(result.Failed))
	for _, failure := range result.Failed {
		failures = append(failures, library.Failure{Path: failure.Path, Reason: failure.Reason})
	}

	lib := library.FromTracks(dir, result.Tracks, failures)
	a.adopt(lib, phone)

	return ScanResult{
		Root:      dir,
		Source:    "phone",
		Summary:   lib.Summarize(),
		Errors:    lib.Errors,
		Cancelled: cancelled,
	}, nil
}

// adopt makes a freshly scanned collection the one being worked on.
func (a *App) adopt(lib *library.Library, phone *device.Phone) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.lib = lib
	a.phone = phone
	a.changes = nil
	a.manual = map[string]tags.Edit{}

	// The files a playlist is looked up among are listed again for the new
	// library, the next time a playlist needs them.
	a.lists.mu.Lock()
	a.lists.index = nil
	a.lists.mu.Unlock()
}

// EditTracks records an edit against every listed track. A nil field is left
// alone, so the table can set one column across a selection without touching
// the rest.
func (a *App) EditTracks(paths []string, edit tags.Edit) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.manual == nil {
		a.manual = map[string]tags.Edit{}
	}
	for _, path := range paths {
		a.manual[path] = merge(a.manual[path], edit)
	}
}

// ClearEdits drops the hand edits, returning to what the rules alone would do.
func (a *App) ClearEdits() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.manual = map[string]tags.Edit{}
}

// merge lays a new edit over an earlier one, field by field.
func merge(base, over tags.Edit) tags.Edit {
	for _, field := range []struct{ into, from **string }{
		{&base.Artist, &over.Artist},
		{&base.AlbumArtist, &over.AlbumArtist},
		{&base.Album, &over.Album},
		{&base.Title, &over.Title},
		{&base.Genre, &over.Genre},
		{&base.Lyrics, &over.Lyrics},
	} {
		if *field.from != nil {
			*field.into = *field.from
		}
	}
	for _, number := range []struct{ into, from **int }{
		{&base.Year, &over.Year},
		{&base.TrackNo, &over.TrackNo},
		{&base.DiscNo, &over.DiscNo},
	} {
		if *number.from != nil {
			*number.into = *number.from
		}
	}
	base.RemoveCompilation = base.RemoveCompilation || over.RemoveCompilation
	base.RemoveAlbumArtistSort = base.RemoveAlbumArtistSort || over.RemoveAlbumArtistSort
	return base
}

/* Planning ---------------------------------------------------------------- */

// PreviewResult shows what a set of rules would do before anything is written.
type PreviewResult struct {
	Summary plan.Summary  `json:"summary"`
	Changes []plan.Change `json:"changes"`
	Shown   int           `json:"shown"`
}

// Preview builds the plan and keeps it, so Apply writes exactly what was shown.
func (a *App) Preview(rules plan.Rules) (PreviewResult, error) {
	// The edits are copied: plan.Build reads them without the lock, while
	// EditTracks may be writing to the map from another call.
	a.mu.Lock()
	lib, manual := a.lib, maps.Clone(a.manual)
	a.mu.Unlock()

	if lib == nil {
		return PreviewResult{}, errors.New("scan a library first")
	}

	changes := plan.Build(lib, rules, manual)

	a.mu.Lock()
	a.changes = changes
	a.mu.Unlock()

	shown := changes
	if len(shown) > previewLimit {
		shown = shown[:previewLimit]
	}
	return PreviewResult{
		Summary: plan.Summarize(changes),
		Changes: shown,
		Shown:   len(shown),
	}, nil
}

// ApplyReport is the outcome of a run.
type ApplyReport struct {
	Written   int           `json:"written"`
	Failed    []plan.Result `json:"failed"`
	Cancelled bool          `json:"cancelled"`
}

// Apply writes the plan built by the last Preview.
func (a *App) Apply() (ApplyReport, error) {
	a.mu.Lock()
	changes := a.changes
	a.mu.Unlock()

	if len(changes) == 0 {
		return ApplyReport{}, errors.New("nothing to apply")
	}
	ctx := a.begin()

	items := make([]wire.Item, 0, len(changes))
	for _, change := range changes {
		items = append(items, wire.Item{Path: change.Path, Edit: change.Edit()})
	}

	report := ApplyReport{Failed: []plan.Result{}}
	emit := a.stages("apply:progress")

	err := a.write(ctx, items, emit, func(written device.Written) {
		if written.Error != "" {
			report.Failed = append(report.Failed, plan.Result{Path: written.Path, Error: written.Error})
		} else {
			report.Written++
		}
	})

	report.Cancelled = errors.Is(err, context.Canceled)
	if err != nil && !report.Cancelled {
		return report, err
	}
	if a.onPhone() {
		emit(stagePlaylists, device.Progress{})
	}
	a.finished(ctx)

	a.mu.Lock()
	a.changes = nil
	a.mu.Unlock()

	return report, nil
}

// The stages of writing, as the interface is told about them. On a phone the
// writing itself is the quick part: having the media database read the files
// again and putting the playlists back take longer, and a progress bar that
// stopped at the last file written looked like a hang.
const (
	stageWrite     = "write"
	stageRescan    = "rescan"
	stagePlaylists = "playlists"
)

// stages reports progress under an event, a few times a second — but always
// the first report of a stage and the last of it, so the interface never sits
// on a number the job has long since passed.
func (a *App) stages(event string) func(string, device.Progress) {
	var (
		mu    sync.Mutex
		stage string
		last  time.Time
	)
	return func(current string, p device.Progress) {
		mu.Lock()
		if current == stage && p.Done < p.Total && time.Since(last) < progressInterval {
			mu.Unlock()
			return
		}
		stage, last = current, time.Now()
		mu.Unlock()

		wr.EventsEmit(a.ctx, event, map[string]any{"stage": current, "done": p.Done, "total": p.Total})
	}
}

// write sends a batch of edits wherever the collection lives. report, which
// may be nil, hears how far each stage has got.
func (a *App) write(ctx context.Context, items []wire.Item, report func(string, device.Progress), onFile func(device.Written)) error {
	a.mu.Lock()
	phone := a.phone
	a.mu.Unlock()

	at := func(stage string) func(device.Progress) {
		if report == nil {
			return nil
		}
		return func(p device.Progress) { report(stage, p) }
	}

	if phone != nil {
		if err := phone.Apply(ctx, items, at(stageWrite), onFile); err != nil {
			return err
		}
		// The phone's media database still holds the old tags until it is told
		// to look again. Its playlists are put right by finished(), once the
		// whole job is over.
		paths := make([]string, 0, len(items))
		for _, item := range items {
			paths = append(paths, item.Path)
		}
		phone.Rescan(ctx, paths, at(stageRescan))
		return nil
	}

	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}

		written := device.Written{Path: item.Path}
		if err := tags.Write(item.Path, item.Edit); err != nil {
			written.Error = err.Error()
		}
		if onFile != nil {
			onFile(written)
		}
		if report != nil {
			report(stageWrite, device.Progress{Done: i + 1, Total: len(items)})
		}
	}
	return nil
}

/* Lyrics ------------------------------------------------------------------ */

// LyricsOptions selects which tracks to look lyrics up for.
type LyricsOptions struct {
	// OnlyMissing skips tracks that already carry lyrics.
	OnlyMissing bool `json:"onlyMissing"`
	// Artists limits the run to these artist names; empty means all of them.
	Artists []string `json:"artists"`
	Backup  bool     `json:"backup"`
	// PreferSynced embeds the timed copy of the words where a source has one.
	//
	// Phonograph picks what to show with
	//     all.indexOfFirst { it is LrcLyrics }
	// over the embedded lyrics and any file beside the track, and shows
	// nothing at all when that finds nothing — which is why plain words in the
	// tag sit behind a menu. Timed words in the same tag are chosen at once.
	PreferSynced bool `json:"preferSynced"`
}

// LyricsResult is one track's outcome, streamed to the interface as it happens.
type LyricsResult struct {
	Path   string `json:"path"`
	Artist string `json:"artist"`
	Title  string `json:"title"`
	Source string `json:"source"`
	Status string `json:"status"` // "found" | "missing" | "error" | "unwritten"
	Detail string `json:"detail,omitempty"`
	// Synced says the words that went in carry timings.
	Synced bool `json:"synced,omitempty"`
}

// LyricsReport totals a lyrics run.
type LyricsReport struct {
	Found     int  `json:"found"`
	Missing   int  `json:"missing"`
	Failed    int  `json:"failed"`
	Synced    int  `json:"synced"`
	Total     int  `json:"total"`
	Cancelled bool `json:"cancelled"`
}

// FetchLyrics looks up lyrics for the selected tracks and embeds them.
//
// Looking up is slow and paced by the sources' own rate limits, while writing
// to a phone costs a round trip, so the two are kept apart: several lookups run
// at once and what they find is written back in batches.
func (a *App) FetchLyrics(opts LyricsOptions) (LyricsReport, error) {
	a.mu.Lock()
	lib := a.lib
	a.mu.Unlock()

	if lib == nil {
		return LyricsReport{}, errors.New("scan a library first")
	}

	targets := selectTracks(lib, opts)
	report := LyricsReport{Total: len(targets)}
	if len(targets) == 0 {
		return report, nil
	}

	ctx := a.begin()
	// writeCtx is what batches are written with; the last one gets a context
	// of its own, since a stopped run cancels ctx before it is written.
	writeCtx := ctx
	workers := min(runtime.NumCPU(), 4)

	var (
		mu      sync.Mutex
		done    int
		pending []wire.Item
		synced  = map[string]bool{}
		wg      sync.WaitGroup
		queue   = make(chan tags.Track)
		// writing keeps batches going to the phone one at a time. Each batch
		// travels as the same plan file, so two at once overwrite each other
		// and one of them is lost.
		writing sync.Mutex
	)

	emit := throttle(func(n int) {
		wr.EventsEmit(a.ctx, "lyrics:progress", map[string]int{"done": n, "total": len(targets)})
	})

	// flush writes what has been found so far. It is called holding the lock,
	// and releases it while the writing happens so lookups keep running.
	flush := func() {
		batch := pending
		pending = nil
		if len(batch) == 0 {
			return
		}

		mu.Unlock()
		writing.Lock()
		err := a.write(writeCtx, batch, nil, func(written device.Written) {
			if written.Error == "" {
				return
			}
			// The track was reported found when its lookup finished; now it
			// is taken back out of the totals.
			mu.Lock()
			report.Found--
			report.Failed++
			if synced[written.Path] {
				report.Synced--
			}
			mu.Unlock()
			wr.EventsEmit(a.ctx, "lyrics:track", LyricsResult{
				Path: written.Path, Status: "unwritten", Detail: written.Error,
			})
		})
		writing.Unlock()
		mu.Lock()

		if err != nil && !errors.Is(err, context.Canceled) {
			wr.EventsEmit(a.ctx, "lyrics:track", LyricsResult{Status: "error", Detail: err.Error()})
		}

	}

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for track := range queue {
				result, found := a.lookup(ctx, track)

				mu.Lock()
				switch result.Status {
				case "found":
					report.Found++

					text := found.Lyrics
					if opts.PreferSynced && found.Synced != "" {
						text = found.Synced
						report.Synced++
						synced[track.Path] = true
						result.Synced = true
					}
					pending = append(pending, wire.Item{
						Path: track.Path,
						Edit: tags.Edit{Lyrics: tags.Str(text), Backup: opts.Backup},
					})
					if len(pending) >= lyricsBatch {
						flush()
					}
				case "missing":
					report.Missing++
				default:
					report.Failed++
				}
				done++
				current := done
				mu.Unlock()

				wr.EventsEmit(a.ctx, "lyrics:track", result)
				emit(current)
			}
		}()
	}

	var cancelled bool
	for _, track := range targets {
		select {
		case <-ctx.Done():
			cancelled = true
		case queue <- track:
			continue
		}
		break
	}
	close(queue)
	wg.Wait()

	// Whatever the last batch found still has to be written, cancelled or not.
	// This and the playlists below take a while on a phone, with every track
	// already counted, so the interface is told what is going on.
	wr.EventsEmit(a.ctx, "lyrics:phase", "Writing the last tracks…")
	last, stopLast := context.WithTimeout(a.ctx, 3*time.Minute)
	writeCtx = last
	mu.Lock()
	flush()
	mu.Unlock()
	stopLast()

	if a.onPhone() {
		wr.EventsEmit(a.ctx, "lyrics:phase", "Putting the playlists back on the phone…")
	}

	// The playlists are put right even when the run was stopped half way, since
	// what was written up to then detached those tracks all the same. A
	// cancelled context would refuse the commands, so this one is its own.
	settle, stop := context.WithTimeout(a.ctx, 3*time.Minute)
	a.finished(settle)
	stop()

	report.Cancelled = cancelled || ctx.Err() != nil
	return report, nil
}

// lookup finds lyrics for one track, returning what to show and what to write.
func (a *App) lookup(ctx context.Context, track tags.Track) (LyricsResult, lyrics.Match) {
	query := queryFor(track)
	// The log names the track the way it was searched for, so one with an
	// empty title shows the name from its file rather than nothing.
	result := LyricsResult{Path: track.Path, Artist: track.Artist, Title: lyrics.DisplayTitle(query)}
	if result.Artist == "" {
		result.Artist = track.AlbumArtist
	}

	match, err := a.finder.Find(ctx, query)
	switch {
	case errors.Is(err, context.Canceled):
		result.Status, result.Detail = "error", "cancelled"
		return result, lyrics.Match{}
	case errors.Is(err, lyrics.ErrNotFound):
		result.Status = "missing"

		// Whatever came closest is named, so a track the databases file under
		// another name can be finished by hand from the track menu.
		var missing *lyrics.NotFound
		if errors.As(err, &missing) && len(missing.Unanswered) > 0 {
			// Not found where it was looked for, but a source did not answer:
			// an error worth another try, not a verdict.
			result.Status = "error"
			result.Detail = "not found, but " + strings.Join(missing.Unanswered, "; ") + " — try again later"
			return result, lyrics.Match{}
		}
		if errors.As(err, &missing) && missing.Closest.Title != "" {
			result.Detail = fmt.Sprintf("closest: %s — %s", missing.Closest.Artist, missing.Closest.Title)
		}
		return result, lyrics.Match{}
	case err != nil:
		result.Status, result.Detail = "error", err.Error()
		return result, lyrics.Match{}
	}

	result.Status = "found"
	result.Source = match.Source
	result.Detail = fmt.Sprintf("%s — %s", match.Artist, match.Title)
	return result, match
}

// LyricsPreview is what one pasted link turned out to hold.
type LyricsPreview struct {
	Source string `json:"source"`
	URL    string `json:"url"`
	Lyrics string `json:"lyrics"`
}

// LyricsFromLink reads the lyrics off a page the user found themselves.
//
// Searching cannot place every track: a song filed under a transliteration of
// its artist, or under the name of whoever leaked it, is unreachable from what
// the file says. Pointing at the page settles it, and nothing is written until
// the user has seen what came back.
func (a *App) LyricsFromLink(link string) (LyricsPreview, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()

	match, err := a.finder.FromURL(ctx, link)
	if err != nil {
		return LyricsPreview{}, err
	}
	return LyricsPreview{Source: match.Source, URL: match.URL, Lyrics: match.Lyrics}, nil
}

// queryFor is what the lyrics sources are asked about one track.
func queryFor(track tags.Track) lyrics.Query {
	return lyrics.Query{
		Artist:      track.Artist,
		AlbumArtist: track.AlbumArtist,
		Title:       track.Title,
		Album:       track.Album,
		Duration:    track.Duration,
		Path:        track.Path,
	}
}

// selectTracks narrows the collection down to the tracks a lyrics run covers.
func selectTracks(lib *library.Library, opts LyricsOptions) []tags.Track {
	wanted := map[string]bool{}
	for _, artist := range opts.Artists {
		wanted[artist] = true
	}

	var out []tags.Track
	for _, track := range lib.Tracks {
		if opts.OnlyMissing && track.HasLyrics {
			continue
		}
		// A track with nothing in its title is still searched for by the name
		// of its file; only one that gives a search nothing at all is left out.
		if !lyrics.Searchable(queryFor(track)) {
			continue
		}
		if len(wanted) > 0 && !wanted[track.Artist] && !wanted[track.AlbumArtist] {
			continue
		}
		out = append(out, track)
	}
	return out
}

// DeleteResult is what deleting came to: the files that are gone, or nothing
// at all because the user said no.
type DeleteResult struct {
	Gone      []string `json:"gone"`
	Cancelled bool     `json:"cancelled"`
}

// DeleteUnreadable removes files the scan could not read — damaged downloads,
// most often, that a player cannot play either. Only files the last scan
// reported as unreadable can go this way, whatever the interface asks for,
// and it returns the ones that are gone.
func (a *App) DeleteUnreadable(paths []string) (DeleteResult, error) {
	a.mu.Lock()
	lib, phone := a.lib, a.phone
	a.mu.Unlock()
	if lib == nil {
		return DeleteResult{}, errors.New("scan a library first")
	}

	unreadable := map[string]bool{}
	for _, failure := range lib.Errors {
		unreadable[failure.Path] = true
	}
	var chosen []string
	for _, path := range paths {
		if unreadable[path] {
			chosen = append(chosen, path)
		}
	}
	if len(chosen) == 0 {
		return DeleteResult{Gone: []string{}}, nil
	}
	if !a.confirmDelete(chosen, phone != nil) {
		return DeleteResult{Gone: []string{}, Cancelled: true}, nil
	}

	var gone []string
	if phone != nil {
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
		defer cancel()
		var err error
		if gone, err = phone.Delete(ctx, chosen); err != nil {
			return DeleteResult{}, err
		}
	} else {
		for _, path := range chosen {
			if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
				gone = append(gone, path)
			}
		}
	}

	removed := map[string]bool{}
	for _, path := range gone {
		removed[path] = true
	}
	a.mu.Lock()
	kept := lib.Errors[:0:0]
	for _, failure := range lib.Errors {
		if !removed[failure.Path] {
			kept = append(kept, failure)
		}
	}
	lib.Errors = kept
	a.mu.Unlock()

	if gone == nil {
		gone = []string{}
	}
	return DeleteResult{Gone: gone}, nil
}

// confirmDelete asks once more, in a dialog of the system's own: the page's
// confirm() shows nothing in the Mac's webview and answers no by itself, so a
// button relying on it did nothing at all.
func (a *App) confirmDelete(paths []string, onPhone bool) bool {
	where := "from this computer"
	if onPhone {
		where = "from the phone"
	}
	names := make([]string, 0, 12)
	for i, path := range paths {
		if i == 12 {
			names = append(names, fmt.Sprintf("…and %d more", len(paths)-12))
			break
		}
		names = append(names, path)
	}
	noun := "files"
	if len(paths) == 1 {
		noun = "file"
	}

	answer, err := wr.MessageDialog(a.ctx, wr.MessageDialogOptions{
		Type:          wr.QuestionDialog,
		Title:         fmt.Sprintf("Delete %d %s %s?", len(paths), noun, where),
		Message:       "This cannot be undone.\n\n" + strings.Join(names, "\n"),
		Buttons:       []string{"Delete", "Cancel"},
		DefaultButton: "Cancel",
		CancelButton:  "Cancel",
	})
	// Windows ignores the buttons given and answers Yes or No.
	return err == nil && (answer == "Delete" || answer == "Yes")
}

// RevealFile opens the system file manager with the file selected, which is
// only possible for a library on this computer: a path on a phone belongs to
// no filesystem here.
func (a *App) RevealFile(path string) error {
	a.mu.Lock()
	onPhone := a.phone != nil
	a.mu.Unlock()

	if onPhone {
		return errors.New("this track is on the phone, not on this computer")
	}
	if _, err := os.Stat(path); err != nil {
		return errors.New("the file is no longer there")
	}
	return reveal(path)
}

// Tracks hands the whole library to the table, which does its own sorting,
// filtering and selecting. A few thousand rows of tags is a small payload next
// to the audio they describe, so it is sent once per scan rather than paged.
func (a *App) Tracks() []tags.Track {
	a.mu.Lock()
	lib := a.lib
	a.mu.Unlock()

	if lib == nil {
		return []tags.Track{}
	}
	return lib.Tracks
}

// throttle limits how often a callback runs, while always letting the first
// call through so progress appears immediately.
func throttle[T any](fn func(T)) func(T) {
	var (
		mu   sync.Mutex
		last time.Time
	)
	return func(value T) {
		mu.Lock()
		if time.Since(last) < progressInterval {
			mu.Unlock()
			return
		}
		last = time.Now()
		mu.Unlock()

		fn(value)
	}
}
