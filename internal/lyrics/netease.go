package lyrics

// NetEase Cloud Music is a large streaming catalogue whose public API answers
// without a key. It carries what the other sources often do not — Russian rap,
// leaks uploaded by listeners — and its words come timed line by line, so a
// phone scrolls them with the song. It also reports each track's length, which
// tells two songs of one name apart.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	neteaseSearch = "https://music.163.com/api/search/get"
	neteaseLyric  = "https://music.163.com/api/song/lyric"

	// How many of the songs a search returns are worth asking for words: each
	// costs a request, and the search orders the likely ones first.
	neteaseOpened = 2
)

type netease struct {
	client  *http.Client
	limiter *limiter
}

func (n *netease) name() string { return "NetEase" }

type neteaseSearchResponse struct {
	Result struct {
		Songs []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Duration int64  `json:"duration"` // milliseconds
			Artists  []struct {
				Name string `json:"name"`
			} `json:"artists"`
		} `json:"songs"`
	} `json:"result"`
}

func (n *netease) search(ctx context.Context, q Query) ([]Match, error) {
	query := url.Values{"s": {leadArtist(q.Artist) + " " + q.Title}, "type": {"1"}, "limit": {"8"}}
	var found neteaseSearchResponse
	if err := n.get(ctx, neteaseSearch+"?"+query.Encode(), &found); err != nil {
		return nil, err
	}

	var matches []Match
	opened := 0
	for _, song := range found.Result.Songs {
		names := make([]string, 0, len(song.Artists))
		for _, artist := range song.Artists {
			names = append(names, artist.Name)
		}
		candidate := Match{
			Source:   n.name(),
			Artist:   strings.Join(names, " & "),
			Title:    song.Name,
			URL:      "https://music.163.com/#/song?id=" + strconv.FormatInt(song.ID, 10),
			Duration: time.Duration(song.Duration) * time.Millisecond,
		}
		// Asking for the words costs a request, so only names that already
		// look right are asked about. The search fills its list with anything
		// that shares a word, so the song's name has to be the one asked
		// for, not one that merely looks alike: "O.J. Glove" is not "Glo'd Up".
		if score(q, candidate) < 0.5 || !sameName(q.Title, song.Name) {
			continue
		}
		if opened++; opened > neteaseOpened {
			break
		}
		plain, synced, err := n.words(ctx, song.ID)
		if err != nil {
			return matches, err
		}
		if plain == "" {
			continue
		}
		candidate.Lyrics, candidate.Synced = plain, synced
		matches = append(matches, candidate)
	}
	return matches, nil
}

// words fetches a song's lyrics: the timed lines, and the same words without
// their times.
func (n *netease) words(ctx context.Context, id int64) (plain, synced string, err error) {
	var answer struct {
		NoLyric     bool `json:"nolyric"`
		Uncollected bool `json:"uncollected"`
		Lrc         struct {
			Lyric string `json:"lyric"`
		} `json:"lrc"`
	}
	address := fmt.Sprintf("%s?id=%d&lv=1&kv=1&tv=-1", neteaseLyric, id)
	if err := n.get(ctx, address, &answer); err != nil {
		return "", "", err
	}
	if answer.NoLyric || answer.Uncollected {
		return "", "", nil
	}
	return neteaseWords(answer.Lrc.Lyric)
}

func (n *netease) get(ctx context.Context, address string, into any) error {
	if err := n.limiter.wait(ctx); err != nil {
		return err
	}
	resp, err := fetch(ctx, n.client, n.name(), address, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("NetEase replied %s", resp.Status)
	}
	if err := json.NewDecoder(limitBody(resp)).Decode(into); err != nil {
		return fmt.Errorf("NetEase: %w", err)
	}
	return nil
}

var (
	lrcTime = regexp.MustCompile(`^\s*((?:\[\d{1,3}:\d{2}(?:[.:]\d{1,3})?\])+)\s*(.*)$`)
	// The credits NetEase puts at the top of a song's timed lines, in Chinese
	// or English: "作词 : Ерышев Илья" is "words by".
	lrcCredit = regexp.MustCompile(`(?i)^(?:作词|作曲|编曲|制作人?|混音|母带|录音|和声|监制|出品|发行|企划|统筹|吉他|贝斯|鼓|键盘|弦乐|词|曲|lyricist|lyrics by|composer|composed by|arranger|producer|produced by|mix(?:ed|ing)?(?: by)?|master(?:ed|ing)?(?: by)?|recording)\s*[:：]`)
	// What NetEase writes for a track with no words: "pure music, please enjoy".
	lrcInstrumental = regexp.MustCompile(`纯音乐|此歌曲为没有填词的纯音乐`)
)

// neteaseWords turns NetEase's timed lines into the timed words, without the
// credits, and the plain words beside them.
func neteaseWords(lrc string) (plain, synced string, err error) {
	if lrcInstrumental.MatchString(lrc) {
		return "", "", nil
	}
	var timed, words []string
	for _, line := range strings.Split(strings.ReplaceAll(lrc, "\r", ""), "\n") {
		match := lrcTime.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		text := strings.TrimSpace(match[2])
		if lrcCredit.MatchString(text) {
			continue
		}
		timed = append(timed, match[1]+text)
		words = append(words, text)
	}
	plain = strings.TrimSpace(strings.Join(words, "\n"))
	if plain == "" {
		return "", "", nil
	}
	return plain, strings.Join(timed, "\n"), nil
}

// sameName says whether a catalogue's name for a song is the name asked for,
// give or take its brackets, case and punctuation.
func sameName(asked, found string) bool {
	if similarity(cleanTitle(asked), cleanTitle(found)) >= 0.85 {
		return true
	}
	return similarity(bareTitle(asked), bareTitle(found)) >= 0.85
}
