package lyrics

// Genius is the fallback, and the reason a collection of leaks and unreleased
// tracks gets any lyrics at all: it catalogues material no licensed database
// carries. Its public search endpoint needs no key, but the words themselves
// only exist in the song page, so the page is fetched and the lyrics container
// read out of it.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	geniusSearch = "https://genius.com/api/search/multi"
	userAgent    = "MusicLibraryOrganizer/1.0 (music tagger)"

	// Genius serves full pages, so both the search results and the number of
	// pages fetched per track are kept small.
	geniusCandidates = 5
	maxBodyBytes     = 8 << 20
)

type genius struct {
	client  *http.Client
	limiter *limiter
}

func (g *genius) name() string { return "Genius" }

type geniusResponse struct {
	Response struct {
		Sections []struct {
			Type string `json:"type"`
			Hits []struct {
				Type   string `json:"type"`
				Result struct {
					Title         string `json:"title"`
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
	return matches, nil
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

			out = append(out, Match{
				Source: g.name(),
				Artist: hit.Result.PrimaryArtist.Name,
				Title:  hit.Result.Title,
				URL:    hit.Result.URL,
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
