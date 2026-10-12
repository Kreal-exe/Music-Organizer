package lyrics

// Genius is the fallback, and the reason a collection of leaks and unreleased
// tracks gets any lyrics at all: it catalogues material no licensed database
// carries. Its public search endpoint needs no key, but the words themselves
// only exist in the song page, so the page is fetched and the lyrics container
// read out of it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	geniusSearch = "https://genius.com/api/search/multi"
	geniusSongs  = "https://genius.com/api/songs"
	userAgent    = "MusicLibraryOrganizer/1.0 (music tagger)"

	// Genius serves full pages, so both the search results and the number of
	// pages fetched per track are kept small.
	geniusCandidates = 5
	maxBodyBytes     = 8 << 20
)

type genius struct {
	client  *http.Client
	limiter *limiter

	// Once the search has answered 429 it is left alone for a while: every
	// further request only keeps the ban going, and costs each track the
	// retries' wait.
	mu           sync.Mutex
	restingUntil time.Time
}

// geniusRest is how long the search is left alone after turning a request
// away.
const geniusRest = 3 * time.Minute

func (g *genius) resting() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Now().Before(g.restingUntil)
}

func (g *genius) rest() {
	g.mu.Lock()
	g.restingUntil = time.Now().Add(geniusRest)
	g.mu.Unlock()
}

func (g *genius) name() string { return "Genius" }

type geniusResponse struct {
	Response struct {
		Sections []struct {
			Type string `json:"type"`
			Hits []struct {
				Type   string `json:"type"`
				Result struct {
					ID            int    `json:"id"`
					Title         string `json:"title"`
					ArtistNames   string `json:"artist_names"`
					URL           string `json:"url"`
					PrimaryArtist struct {
						Name string `json:"name"`
					} `json:"primary_artist"`
				} `json:"result"`
			} `json:"hits"`
		} `json:"sections"`
	} `json:"response"`
}

func (g *genius) search(ctx context.Context, q Query) ([]Match, error) {
	hits, err := g.find(ctx, leadArtist(q.Artist)+" "+q.Title)
	if err != nil {
		// The search turns requests away for a while after many in a row,
		// but the song pages are still served, at addresses made from the
		// names — so the page the search would have led to is tried.
		if guessed := g.byAddress(ctx, q); guessed != nil {
			return guessed, nil
		}
		return nil, err
	}

	var matches []Match
	for _, hit := range hits {
		// Fetching a page is expensive, so only the candidates whose names
		// already look right are opened.
		if score(q, hit) < 0.5 {
			continue
		}

		lyrics, err := g.fetchLyrics(ctx, hit.URL)
		if err != nil {
			return matches, err
		}
		if lyrics == "" {
			continue
		}
		hit.Lyrics = lyrics
		matches = append(matches, hit)

		if len(matches) >= 2 {
			break
		}
	}
	// Nothing by name that would be accepted: the title may be a line of
	// the song rather than its name.
	if hookAttempt(q) && !anyAccepted(q, matches) {
		// A failure here costs only the extra chance, not what was found.
		if hooked, err := g.byHook(ctx, q); err == nil {
			matches = append(matches, hooked...)
		}
	}
	// Still nothing: the song may be filed under the other artist of a joint
	// album, which the tag does not name.
	if q.Album != "" && !anyAccepted(q, matches) {
		if found, err := g.byAlbum(ctx, q); err == nil {
			matches = append(matches, found...)
		}
	}
	return matches, nil
}

// byAlbum finds a song by its name alone and takes it if Genius has it on
// the album the tags name. "Почему я иду" is tagged as Кишлак's, from his
// joint album with семьсот семь, and Genius files it under семьсот семь: the
// artists disagree, but a song of that name on that album is that song.
func (g *genius) byAlbum(ctx context.Context, q Query) ([]Match, error) {
	title := bareTitle(q.Title)
	hits, err := g.find(ctx, title)
	if err != nil {
		return nil, err
	}
	asked := 0
	for _, hit := range hits {
		if hit.geniusID == 0 || similarity(title, bareTitle(hit.Title)) < 0.9 {
			continue
		}
		if asked++; asked > 2 {
			break
		}
		album, err := g.album(ctx, hit.geniusID)
		if err != nil {
			return nil, err
		}
		if album == "" || similarity(bareTitle(q.Album), bareTitle(withoutTranslation(album))) < 0.85 {
			continue
		}
		lyrics, err := g.fetchLyrics(ctx, hit.URL)
		if err != nil {
			return nil, err
		}
		if lyrics != "" {
			hit.Lyrics, hit.SameAlbum = lyrics, true
			return []Match{hit}, nil
		}
	}
	return nil, nil
}

// album asks Genius which album a song is on.
func (g *genius) album(ctx context.Context, id int) (string, error) {
	if err := g.limiter.wait(ctx); err != nil {
		return "", err
	}
	resp, err := fetch(ctx, g.client, g.name(), fmt.Sprintf("%s/%d", geniusSongs, id), "application/json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Genius replied %s", resp.Status)
	}
	var parsed struct {
		Response struct {
			Song struct {
				Album *struct {
					Name string `json:"name"`
				} `json:"album"`
			} `json:"song"`
		} `json:"response"`
	}
	if err := json.NewDecoder(limitBody(resp)).Decode(&parsed); err != nil {
		return "", fmt.Errorf("Genius: %w", err)
	}
	if parsed.Response.Song.Album == nil {
		return "", nil
	}
	return parsed.Response.Song.Album.Name, nil
}

// withoutTranslations drops the translations from every name in a credit:
// "Кишлак (Kishlak) & семьсот семь (sem’sot sem’)".
func withoutTranslations(credit string) string {
	parts := strings.Split(credit, " & ")
	for i, part := range parts {
		names := strings.Split(part, ", ")
		for j, name := range names {
			names[j] = withoutTranslation(strings.TrimSpace(name))
		}
		parts[i] = strings.Join(names, ", ")
	}
	return strings.Join(parts, " & ")
}

// byHook finds a song a leak is named after a line of. "Fuck Or Sum (Session
// Edit)" is Genius's "Extra (For Fun) (Sessions)": nothing in the two names is
// alike, and searching for artist and title together does not turn it up, but
// the title alone finds it by its lyrics. Only a song by the same artist whose
// words repeat the title as a hook is taken.
func (g *genius) byHook(ctx context.Context, q Query) ([]Match, error) {
	hook := bareTitle(q.Title)
	hits, err := g.find(ctx, hook)
	if err != nil {
		return nil, err
	}
	opened := 0
	for _, hit := range hits {
		if similarity(leadArtist(q.Artist), leadArtist(hit.Artist)) < 0.9 {
			continue
		}
		if opened++; opened > 2 {
			break
		}
		lyrics, err := g.fetchLyrics(ctx, hit.URL)
		if err != nil {
			return nil, err
		}
		if hookCount(lyrics, hook) >= minHookRepeats {
			hit.Lyrics, hit.Hook = lyrics, true
			return []Match{hit}, nil
		}
	}
	return nil, nil
}

// find asks the search endpoint for songs matching the track.
func (g *genius) find(ctx context.Context, text string) ([]Match, error) {
	if g.resting() {
		return nil, errors.New("Genius is turning requests away for a while (429 Too Many Requests)")
	}
	if err := g.limiter.wait(ctx); err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("q", text)

	resp, err := fetch(ctx, g.client, g.name(), geniusSearch+"?"+query.Encode(), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		g.rest()
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Genius replied %s", resp.Status)
	}

	var parsed geniusResponse
	if err := json.NewDecoder(limitBody(resp)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("Genius: %w", err)
	}

	// A song can be the top hit, or sit in the song section, or be found by a
	// line of its own lyrics — the sections are different views of one result
	// list. All of them are read, because a song that only turns up as the top
	// hit is still the song; the scoring throws out what does not belong.
	var out []Match
	seen := map[string]bool{}

	for _, section := range parsed.Response.Sections {
		for _, hit := range section.Hits {
			if hit.Type != "song" || hit.Result.URL == "" || seen[hit.Result.URL] {
				continue
			}
			seen[hit.Result.URL] = true

			// Every artist credited, not only the first: a joint song is
			// filed under one of them, and the tag may name the other.
			artist := hit.Result.ArtistNames
			if artist == "" {
				artist = hit.Result.PrimaryArtist.Name
			}
			out = append(out, Match{
				Source:   g.name(),
				Artist:   withoutTranslations(artist),
				Title:    withoutTranslation(hit.Result.Title),
				URL:      hit.Result.URL,
				geniusID: hit.Result.ID,
			})
			if len(out) >= geniusCandidates {
				return out, nil
			}
		}
	}
	return out, nil
}

func (g *genius) fetchLyrics(ctx context.Context, pageURL string) (string, error) {
	if err := g.limiter.wait(ctx); err != nil {
		return "", err
	}

	resp, err := fetch(ctx, g.client, g.name(), pageURL, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Genius replied %s", resp.Status)
	}

	body, err := io.ReadAll(limitBody(resp))
	if err != nil {
		return "", fmt.Errorf("Genius: %w", err)
	}
	return extractGeniusLyrics(string(body)), nil
}

var (
	lyricsContainer = regexp.MustCompile(`(?i)<div[^>]*\bdata-lyrics-container="true"[^>]*>`)
	// The contributor header, the advertisements and the "You might also like"
	// panel all sit inside the lyrics container, marked as not part of the
	// text a reader would select.
	excludedBlock = regexp.MustCompile(`(?i)<div[^>]*\bdata-exclude-from-selection="true"[^>]*>`)
	lineBreak     = regexp.MustCompile(`(?i)<br\s*/?>`)
	blockEnd      = regexp.MustCompile(`(?i)</(?:div|p)>`)
	anyTag        = regexp.MustCompile(`<[^>]*>`)
	divTag        = regexp.MustCompile(`(?i)<(/?)div\b`)
)

// extractGeniusLyrics pulls the text out of the lyrics containers on a song
// page. A page carries one container per section, and each is a small tree of
// spans and links, so the div nesting is counted to find where each ends.
func extractGeniusLyrics(page string) string {
	var parts []string

	for offset := 0; offset < len(page); {
		loc := lyricsContainer.FindStringIndex(page[offset:])
		if loc == nil {
			break
		}
		start := offset + loc[1]
		end := matchingDivEnd(page, start)

		if text := htmlToText(dropExcluded(page[start:end])); text != "" {
			parts = append(parts, text)
		}
		offset = end
	}

	return strings.Join(parts, "\n")
}

// dropExcluded removes the sub-trees Genius marks as not part of the lyrics.
func dropExcluded(fragment string) string {
	for {
		loc := excludedBlock.FindStringIndex(fragment)
		if loc == nil {
			return fragment
		}

		end := matchingDivEnd(fragment, loc[1])
		if end < len(fragment) {
			end += len("</div>")
		}
		fragment = fragment[:loc[0]] + fragment[end:]
	}
}

// matchingDivEnd returns the offset of the closing tag that balances the div
// already opened before start.
func matchingDivEnd(page string, start int) int {
	depth := 1
	for _, tag := range divTag.FindAllStringSubmatchIndex(page[start:], -1) {
		closing := tag[3] > tag[2] // The capture group holds "/" on a close.
		if closing {
			depth--
			if depth == 0 {
				return start + tag[0]
			}
		} else {
			depth++
		}
	}
	return len(page)
}

// htmlToText flattens a fragment of markup into the lines a tag field holds.
func htmlToText(fragment string) string {
	text := lineBreak.ReplaceAllString(fragment, "\n")
	text = blockEnd.ReplaceAllString(text, "\n")
	text = anyTag.ReplaceAllString(text, "")
	return strings.TrimSpace(html.UnescapeString(text))
}

// limitBody caps how much of a response is read, so a broken or hostile server
// cannot exhaust memory.
func limitBody(resp *http.Response) io.Reader {
	return io.LimitReader(resp.Body, maxBodyBytes)
}

// byAddress opens the page Genius would have for the song, at the address it
// makes from the artists' and the song's names: "Juice WRLD" and "Hit A Lick"
// are at genius.com/Juice-wrld-hit-a-lick-lyrics, and a song with a guest
// filed first, like Syki's with Juice WRLD, under both names.
func (g *genius) byAddress(ctx context.Context, q Query) []Match {
	lead := leadArtist(q.Artist)
	credits := []string{lead}
	for _, guest := range q.Guests {
		credits = append(credits, lead+" and "+guest, guest+" and "+lead)
	}
	// Only the name this search is for: the plainer readings are searches of
	// their own, which LRCLIB and NetEase get first and answer with timed words.
	titles := []string{cleanTitle(q.Title)}
	tried := 0
	for _, title := range titles {
		for _, credit := range credits {
			address := geniusSlug(credit + " " + title)
			if address == "" {
				continue
			}
			if tried++; tried > 2 {
				return nil
			}
			lyrics, err := g.fetchLyrics(ctx, "https://genius.com/"+address+"-lyrics")
			if err != nil || lyrics == "" {
				continue
			}
			return []Match{{Source: g.name(), Artist: lead, Title: title, Lyrics: lyrics,
				URL: "https://genius.com/" + address + "-lyrics"}}
		}
	}
	return nil
}

var slugBreak = regexp.MustCompile(`[^a-z0-9]+`)

// geniusSlug writes names the way Genius puts them in its addresses, or
// nothing for a name in another alphabet, which Genius transliterates in ways
// that cannot be guessed.
func geniusSlug(names string) string {
	for _, r := range names {
		if r > 127 && unicode.IsLetter(r) {
			return ""
		}
	}
	text := strings.ToLower(names)
	text = strings.NewReplacer("&", " and ", "'", "", "’", "").Replace(text)
	text = strings.Trim(slugBreak.ReplaceAllString(text, "-"), "-")
	if text == "" {
		return ""
	}
	return strings.ToUpper(text[:1]) + text[1:]
}
