// Package lyrics finds song lyrics for a track and hands back plain text ready
// to be embedded in its tags.
//
// Sources are tried in order and the first confident match wins. LRCLIB covers
// released music well but knows almost nothing about leaks and unreleased
// tracks, which is exactly where Genius is strongest, so the two together cover
// far more of a real collection than either alone.
package lyrics

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/text/unicode/norm"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// NotFound says no source had lyrics confident enough to embed, and carries
// the closest thing that did come back.
//
// A collection of unreleased music is full of tracks a database holds under
// somebody else's name — an alias, a side project, whoever it considers the
// lead — and refusing those silently leaves the user with nothing to go on.
// Named, they can be looked at and taken by hand.
type NotFound struct {
	Closest Match
	// Unanswered names the sources that failed while the others found
	// nothing, with why: the track may well be there.
	Unanswered []string
}

func (e *NotFound) Error() string { return ErrNotFound.Error() }
func (e *NotFound) Unwrap() error { return ErrNotFound }

// How close a rejected candidate has to be before it is worth mentioning.
const worthMentioning = 0.45

// ErrNotFound means no source had lyrics confident enough to embed.
var ErrNotFound = errors.New("no lyrics found")

// Query describes the track to look up.
type Query struct {
	Artist string
	Title  string
	// AlbumArtist is the second opinion on who made the track. Files from a
	// download service often carry the service's own name as the artist and
	// the real one only here.
	AlbumArtist string
	Album       string
	Duration    time.Duration

	// Path is the file itself. Its name is the last thing to go on when the
	// tags name the wrong artist: a download keeps "Artist - Title" in the
	// file name long after the artist field has been overwritten.
	Path string

	// Guests are the names credited in the title before it was cleaned up.
	// They are kept because a database files a collaboration under whoever it
	// considers the lead, which is often one of them.
	Guests []string

	// Qualifiers are the words in the title's brackets that say which
	// recording this is — "session" in "Everlasting Love V2 (Session Edit)".
	// A search drops them to find the song at all, so they are kept here to
	// tell that recording's lyrics from the released song's.
	Qualifiers []string
}

// Match is one candidate set of lyrics.
type Match struct {
	Source string  `json:"source"`
	Artist string  `json:"artist"`
	Title  string  `json:"title"`
	Lyrics string  `json:"lyrics"`
	URL    string  `json:"url"`
	Score  float64 `json:"score"`

	// Synced is the same words with a timestamp on every line, when the
	// source has them, as LRC lines. Phone players show these first, and
	// scroll them with the song; when asked for, they go into the ordinary
	// lyrics tag in place of the plain words.
	Synced string `json:"-"`

	// Duration is the candidate track length, when the source reports one.
	Duration time.Duration `json:"-"`

	// Hook says the source found this song by its lyrics repeating the
	// queried title, not by its name.
	Hook bool `json:"-"`

	// SameAlbum says the source found this song under the same name on the
	// album the track's tags name.
	SameAlbum bool `json:"-"`

	// geniusID is the song's number on Genius, for asking about its album.
	geniusID int
}

// provider is one lyrics source.
type provider interface {
	name() string
	search(ctx context.Context, q Query) ([]Match, error)
}

// Finder looks up lyrics across the configured sources.
type Finder struct {
	// links reads the lyrics off one page, for a link the user found.
	links *pageReader

	// rounds are the sources in the order they are asked. Everything in one
	// round is tried for every reading of the track's name before the next
	// round starts, so a source that can only confirm a name it is given never
	// answers ahead of one that can actually search.
	rounds [][]provider

	// MinScore is how close a result's artist and title must be before it is
	// accepted. Embedding the wrong lyrics is worse than embedding none.
	MinScore float64
}

// NewFinder builds a finder over the default sources.
func NewFinder() *Finder {
	client := &http.Client{Timeout: 20 * time.Second}

	pages := &genius{client: client, limiter: newLimiter(700 * time.Millisecond)}

	return &Finder{
		links: &pageReader{client: client, limiter: newLimiter(700 * time.Millisecond)},
		rounds: [][]provider{
			{
				&lrclib{client: client, limiter: newLimiter(250 * time.Millisecond)},
				// NetEase before Genius: it answers quickly, has timed words, and
				// every track it places is one request fewer to Genius, which
				// turns requests away after many in a row.
				&netease{client: client, limiter: newLimiter(150 * time.Millisecond)},
				// Genius is only asked about tracks LRCLIB could not place, and
				// is paced more slowly because it serves full web pages.
				pages,
			},
			{
				// A round of its own, and last: it cannot search, only confirm
				// the name it is handed, so anything it says is taken on trust
				// and must not get in ahead of a source that looked.
				&lyricsovh{client: client, limiter: newLimiter(400 * time.Millisecond)},
			},
		},
		MinScore: defaultMinScore,
	}
}

// Find returns the best lyrics for a track, or ErrNotFound.
func (f *Finder) Find(ctx context.Context, q Query) (Match, error) {
	searches := attempts(q)
	if len(searches) == 0 {
		return Match{}, ErrNotFound
	}

	var firstErr error
	answered := 0
	failed := map[string]bool{}
	var unanswered []string
	closest := Match{}
	// A match that is the right song but not the recording the title names —
	// the released "Everlasting Love" for a session edit of it — is kept in
	// case nothing better turns up, and the search goes on for the recording.
	fallback := Match{}

	for r, round := range f.rounds {
		for a, attempt := range searches {
			// The last-resort sources cannot search, only answer about the
			// exact name; asking them about every other reading only costs
			// time, and lyrics.ovh takes seconds to say no.
			if r > 0 && a > 0 {
				break
			}
			for _, p := range round {
				if err := ctx.Err(); err != nil {
					return Match{}, err
				}

				results, err := p.search(ctx, attempt)
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					// lyrics.ovh cannot search and is often down; its
					// failing says nothing about whether the track is there.
					if ctx.Err() == nil && !failed[p.name()] && p.name() != "lyrics.ovh" {
						failed[p.name()] = true
						unanswered = append(unanswered, err.Error())
					}
					continue
				}
				answered++

				best := Match{}
				for _, candidate := range results {
					candidate.Score = score(attempt, candidate)
					if candidate.Score > closest.Score {
						closest = candidate
					}
					if candidate.Lyrics != "" && candidate.Score > best.Score {
						best = candidate
					}
				}
				if best.Score >= f.MinScore {
					best.Lyrics = tidyLyrics(best.Lyrics)
					switch {
					case best.Lyrics == "":
					case hasQualifiers(attempt.Qualifiers, best.Title):
						return best, nil
					case best.Score > fallback.Score:
						fallback = best
					}
				}
			}
		}
	}

	if fallback.Lyrics != "" {
		return fallback, nil
	}

	// A source that fell over is only worth reporting if no source answered at
	// all; otherwise the track was searched for properly and simply is not
	// there, and saying "503" would send the user looking for a fault.
	if answered == 0 && firstErr != nil {
		return Match{}, firstErr
	}
	// A source that failed while the rest found nothing may have had it —
	// Genius turning requests away for a while is the usual case — so that
	// is said rather than a plain "not found".
	notFound := &NotFound{Unanswered: unanswered}
	if closest.Score >= worthMentioning {
		notFound.Closest = closest
	}
	if notFound.Closest.Title == "" && len(unanswered) == 0 {
		return Match{}, ErrNotFound
	}
	return Match{}, notFound
}

// attempts lists the searches worth making for one track, best first.
//
// The tags of a real collection are wrong in two ordinary ways: the artist
// field holds something that is not an artist, and the title carries a
// qualifier no database knows about. Each is answered by one more search
// rather than by guessing, and the whole list is only worked through while
// nothing has been found.
func attempts(q Query) []Query {
	var artists []string
	add := func(raw string) {
		name := leadArtist(strings.TrimSpace(raw))
		if name == "" || placeholderArtist(name) {
			return
		}
		for _, have := range artists {
			if fold(have) == fold(name) {
				return
			}
		}
		artists = append(artists, name)
	}
	add(q.Artist)
	add(q.AlbumArtist)

	fileArtist, fileTitle := nameFromFile(q.Path)
	add(fileArtist)

	if len(artists) == 0 {
		// Every name was a stand-in, so the tag is all there is to go on.
		for _, raw := range []string{q.Artist, q.AlbumArtist} {
			if name := leadArtist(strings.TrimSpace(raw)); name != "" {
				artists = append(artists, name)
				break
			}
		}
	}

	// A file with no title in its tags still has one in its name: "10AGE -
	// Близко.mp3" is searched for as Близко, as it would be if it were tagged.
	title := strings.TrimSpace(q.Title)
	if title == "" {
		title = fileTitle
	}

	// The title may repeat the artist, and both readings of it are searched
	// for with that taken off.
	named := withoutLeadingArtist(title, append(artists, q.Artist, fileArtist))

	// The recording's own words go into a search of their own, before the
	// bare name: a database that has the session files it as "Everlasting
	// Love (Sessions)", which the bare name ranks below the released song.
	q.Qualifiers = qualifiers(named)

	titles := []string{cleanTitle(named)}
	bare := bareTitle(named)
	if len(q.Qualifiers) > 0 {
		titles = append(titles, bare+" "+strings.Join(q.Qualifiers, " "))
	}
	if !strings.EqualFold(bare, titles[0]) {
		titles = append(titles, bare)
	}
	titles = append(titles, aliases(named)...)
	if fileTitle = cleanTitle(withoutLeadingArtist(fileTitle, artists)); fileTitle != "" {
		titles = append(titles, fileTitle)
	}

	if len(artists) == 0 || titles[0] == "" {
		return nil
	}

	guests := guestsIn(q.Title + " " + q.Artist)
	// "семьсот семь, Кишлак - раздевайся.mp3": a song with two names on it may
	// be filed under the second.
	for _, credit := range []string{q.Artist, q.AlbumArtist, fileArtist} {
		guests = append(guests, otherArtists(credit)...)
	}

	var out []Query
	seen := map[string]bool{}
	pair := func(artist, title string) {
		if artist == "" || title == "" || len(out) >= maxAttempts {
			return
		}
		key := fold(artist) + "\x00" + fold(title)
		if seen[key] {
			return
		}
		seen[key] = true

		attempt := q
		// Names read off a Mac's file names often spell "й" as two
		// characters; a search engine given those finds other songs.
		attempt.Artist, attempt.Title, attempt.Guests = norm.NFC.String(artist), norm.NFC.String(title), guests
		attempt.Qualifiers = q.Qualifiers
		out = append(out, attempt)
	}

	for _, title := range titles {
		for _, artist := range artists {
			pair(artist, title)
		}
	}
	return out
}

// Searchable says whether a track gives a search anything to go on — an
// artist and a title, from its tags or from its file name.
func Searchable(q Query) bool { return len(attempts(q)) > 0 }

// DisplayTitle is the title a track is searched for under: its own, or the one
// its file name carries when the tags have none.
func DisplayTitle(q Query) string {
	if title := strings.TrimSpace(q.Title); title != "" {
		return title
	}
	_, title := nameFromFile(q.Path)
	return title
}

// How many searches one track is worth. Every reading beyond the first is only
// reached when everything before it came back empty, but a library of
// thousands of tracks is scanned one request at a time, so the list has to end.
const maxAttempts = 5

// nameFromFile reads "Artist - Title" out of a file name. A tagger can write
// anything into the fields; the name a file was downloaded under is the one
// thing that was true when it was made.
func nameFromFile(path string) (artist, title string) {
	name := filepath.Base(strings.ReplaceAll(path, `\`, "/"))
	if name == "." || name == "/" {
		return "", ""
	}
	name = strings.TrimSuffix(name, filepath.Ext(name))

	// A name written with underscores for spaces is a download's or a
	// studio's, "Juice_WRLD_Tell_Me_U_Luv_Me_BPM_128_Stem_Mix_Master_v1": it
	// is read with spaces, and what follows the song's name — the tempo, the
	// stems, the mix — is left off.
	underscored := strings.Count(name, "_") >= 2 || (strings.Contains(name, " ") && strings.Contains(name, "_"))
	if underscored {
		name = strings.Join(strings.Fields(strings.ReplaceAll(name, "_", " ")), " ")
	}
	name = leadingNumber.ReplaceAllString(strings.TrimSpace(name), "")

	artist, title = "", strings.TrimSpace(name)
	for _, sep := range []string{" — ", " – ", " - ", " -- "} {
		if before, after, found := strings.Cut(name, sep); found {
			artist, title = strings.TrimSpace(before), strings.TrimSpace(after)
			break
		}
	}
	if underscored {
		title = beforeStudioWords(title)
	}
	return artist, title
}

// studioWord starts the part of a studio file's name that is about the file,
// not the song: "BPM 128", "Stem Mix", "Master v1", "prod by".
var studioWord = regexp.MustCompile(`(?i)^(?:bpm|\d+bpm|stems?|mix|mixed|master(?:ed)?|prod|produced|session|sessions|cdq|snippet|v\d+|ver|version|edit|final|rough)$`)

// beforeStudioWords keeps a studio file's name up to its first word about
// the file.
func beforeStudioWords(title string) string {
	words := strings.Fields(title)
	for i, word := range words {
		if i == 0 {
			continue
		}
		// A date or a take number at the end, "In The Mix 7 3 18": a number
		// that ends the name or is followed by another.
		number := plainNumber.MatchString(word) && (i == len(words)-1 || plainNumber.MatchString(words[i+1]))
		if studioWord.MatchString(word) || number {
			return strings.Join(words[:i], " ")
		}
	}
	return title
}

var plainNumber = regexp.MustCompile(`^\d{1,4}$`)

// How long to wait before trying a request again. A free service under load
// answers 503 for a moment and is there again straight after, and a library of
// thousands of tracks meets that moment often.
var retryDelays = []time.Duration{time.Second, 4 * time.Second}

// fetch performs a GET, retrying while the answer is a hiccup rather than an
// answer. The response is handed back open for the caller to read and close.
func fetch(ctx context.Context, client *http.Client, source, target, accept string) (*http.Response, error) {
	var last error

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}

		resp, err := client.Do(req)
		switch {
		case err != nil:
			last = fmt.Errorf("%s: %w", source, err)
		case !worthRetrying(resp.StatusCode):
			return resp, nil
		default:
			resp.Body.Close()
			last = fmt.Errorf("%s replied %s", source, resp.Status)
		}

		if attempt >= len(retryDelays) {
			return nil, last
		}
		timer := time.NewTimer(retryDelays[attempt])
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
}

// worthRetrying reports whether a status means "not now" rather than "no".
func worthRetrying(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// limiter spaces out requests to one host, so a scan of a large library stays a
// polite trickle rather than a burst.
type limiter struct {
	gap  time.Duration
	last chan time.Time
}

func newLimiter(gap time.Duration) *limiter {
	last := make(chan time.Time, 1)
	last <- time.Time{}
	return &limiter{gap: gap, last: last}
}

func (l *limiter) wait(ctx context.Context) error {
	select {
	case previous := <-l.last:
		delay := l.gap - time.Since(previous)
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				l.last <- previous
				return ctx.Err()
			}
		}
		l.last <- time.Now()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FromURL reads the lyrics off one page, for a track the search cannot place.
//
// Some songs are filed under a name nothing in the file resembles — a Russian
// act catalogued under a transliteration, a leak credited to whoever leaked
// it — and no amount of searching will bridge that. Pointing at the page is
// the answer, and it is also the one case where a match needs no scoring: the
// user has already decided this is the song.
func (f *Finder) FromURL(ctx context.Context, link string) (Match, error) {
	address, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" {
		return Match{}, errors.New("that is not a web address")
	}

	source, text, err := f.links.read(ctx, address)
	if err != nil {
		return Match{}, err
	}
	if text = tidyLyrics(text); text == "" {
		return Match{}, errNoWords
	}

	return Match{Source: source, URL: address.String(), Lyrics: text, Score: 1}, nil
}
