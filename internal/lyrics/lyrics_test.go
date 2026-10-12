package lyrics

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"Lucid Dreams":                        "Lucid Dreams",
		"Lucid Dreams (Official Audio)":       "Lucid Dreams",
		"03. Lucid Dreams":                    "Lucid Dreams",
		"Righteous [Prod. Nick Mira]":         "Righteous",
		"Bad Energy (feat. Future)":           "Bad Energy",
		"Bad Energy feat. Future":             "Bad Energy",
		"Wandered To LA (Unreleased) (CDQ)":   "Wandered To LA",
		"Bohemian Rhapsody - Remastered 2011": "Bohemian Rhapsody",
		"Come & Go":                           "Come & Go",
		"Всё идёт по плану":                   "Всё идёт по плану",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, expected %q", in, got, want)
		}
	}
}

func TestLeadArtist(t *testing.T) {
	cases := map[string]string{
		"Juice WRLD":                    "Juice WRLD",
		"Juice WRLD feat. Trippie Redd": "Juice WRLD",
		"Juice WRLD & Young Thug":       "Juice WRLD",
		"Eminem, Dr. Dre":               "Eminem",
	}
	for in, want := range cases {
		if got := leadArtist(in); got != want {
			t.Errorf("leadArtist(%q) = %q, expected %q", in, got, want)
		}
	}
}

func TestScorePrefersTheRightSong(t *testing.T) {
	q := Query{Artist: "Juice WRLD", Title: "Lucid Dreams", Duration: 4 * time.Minute}

	right := Match{Artist: "Juice WRLD", Title: "Lucid Dreams", Duration: 4 * time.Minute}
	wrong := Match{Artist: "Taylor Swift", Title: "Lover"}
	near := Match{Artist: "Juice WRLD", Title: "Lucid Dreams (Remix)"}

	if score(q, right) < 0.9 {
		t.Errorf("an exact match scored %.2f", score(q, right))
	}
	if score(q, wrong) > 0.4 {
		t.Errorf("an unrelated song scored %.2f", score(q, wrong))
	}
	if score(q, near) <= score(q, wrong) {
		t.Error("a remix must score above an unrelated song")
	}
}

func TestTidyLyricsRejectsStubs(t *testing.T) {
	if got := tidyLyrics("[Instrumental]"); got != "" {
		t.Errorf("an instrumental was accepted: %q", got)
	}
	if got := tidyLyrics("   "); got != "" {
		t.Errorf("empty lyrics were accepted: %q", got)
	}

	const real = "First line of a song\nSecond line of a song\n\n\n\nThird line"
	got := tidyLyrics(real)
	if strings.Contains(got, "\n\n\n") {
		t.Error("blank runs were not collapsed")
	}
	if !strings.HasPrefix(got, "First line") {
		t.Errorf("the lyrics were mangled: %q", got)
	}
}

func TestExtractGeniusLyrics(t *testing.T) {
	// The shape of a real page: the container also holds a contributor header
	// and a recommendations panel, both marked as excluded from selection.
	const page = `<html><body>
<div>ignored preamble</div>
<div data-lyrics-container="true" class="Lyrics__Container">
<div data-exclude-from-selection="true" class="LyricsHeader"><div>566 Contributors</div>Lucid Dreams Lyrics</div>
[Verse 1]<br/>I still see your shadows<a href="/x"><span>in my room</span></a><br/>
<div class="ReferentFragment"><span>Can&#39;t take back the love</span></div><br/>
<div data-exclude-from-selection="true"><aside>You might also like</aside></div>
</div>
<div data-lyrics-container="true"></div>
<div data-lyrics-container="true">[Chorus]<br/>You left me falling</div>
<div>footer</div>
</body></html>`

	got := extractGeniusLyrics(page)

	for _, want := range []string{
		"[Verse 1]",
		"I still see your shadows",
		"in my room",
		"Can't take back the love",
		"[Chorus]",
		"You left me falling",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the lyrics are missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"preamble", "footer", "Contributors", "Lucid Dreams Lyrics", "You might also like"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("extra content was captured (%q):\n%s", unwanted, got)
		}
	}
	if strings.Contains(got, "<") {
		t.Errorf("markup was not stripped:\n%s", got)
	}
}

// TestFindAgainstLiveSources talks to the real services. It is skipped under
// -short, and treats a network failure as a skip rather than a failure.
func TestFindAgainstLiveSources(t *testing.T) {
	if testing.Short() {
		t.Skip("network test skipped")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	finder := NewFinder()

	cases := []struct {
		name   string
		query  Query
		expect string // A phrase the real lyrics must contain.
	}{
		{
			name:   "released track",
			query:  Query{Artist: "Juice WRLD", Title: "Lucid Dreams"},
			expect: "shadows",
		},
		{
			name:   "guest credit in the tag",
			query:  Query{Artist: "Eminem feat. Rihanna", Title: "Love The Way You Lie"},
			expect: "lie",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			match, err := finder.Find(ctx, tc.query)
			if err != nil {
				t.Skipf("source unavailable: %v", err)
			}

			t.Logf("source %s, score %.2f: %s / %s", match.Source, match.Score, match.Artist, match.Title)
			if !strings.Contains(strings.ToLower(match.Lyrics), tc.expect) {
				t.Errorf("the lyrics are missing %q; starts:\n%.300s", tc.expect, match.Lyrics)
			}
		})
	}
}

// TestGeniusDirectly exercises the fallback on its own, since LRCLIB answers
// first for anything released and would otherwise hide a broken Genius.
func TestGeniusDirectly(t *testing.T) {
	if testing.Short() {
		t.Skip("network test skipped")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	g := &genius{client: &http.Client{Timeout: 20 * time.Second}, limiter: newLimiter(700 * time.Millisecond)}

	q := Query{Artist: "Juice WRLD", Title: "Lucid Dreams"}
	matches, err := g.search(ctx, q)
	if err != nil {
		t.Skipf("Genius unavailable: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("Genius returned nothing")
	}

	best := matches[0]
	t.Logf("%s — %s (%s)", best.Artist, best.Title, best.URL)
	t.Logf("lyrics start:\n%.200s", best.Lyrics)

	if !strings.Contains(strings.ToLower(best.Lyrics), "shadows") {
		t.Errorf("these do not look like real lyrics:\n%.400s", best.Lyrics)
	}
	if strings.Contains(best.Lyrics, "<") {
		t.Error("markup was left in the lyrics")
	}
}

func TestBareTitleStripsQualifiers(t *testing.T) {
	cases := map[string]string{
		"Deprived (Session Edit)":   "Deprived",
		"Devil Horns (v1)":          "Devil Horns",
		"Devil Horns v2":            "Devil Horns",
		"GANG GREEN [Prod. Thraxx]": "GANG GREEN",
		"Lucid Dreams":              "Lucid Dreams",
		"(Intro)":                   "(Intro)",
		"Всё идёт по плану":         "Всё идёт по плану",
	}
	for in, want := range cases {
		if got := bareTitle(in); got != want {
			t.Errorf("bareTitle(%q) = %q, expected %q", in, got, want)
		}
	}
}

func TestPlaceholderArtist(t *testing.T) {
	for _, name := range []string{"VK", "vk.com", "Various Artists", "Unknown Artist", "Сборник"} {
		if !placeholderArtist(name) {
			t.Errorf("%q was taken for an artist", name)
		}
	}
	for _, name := range []string{"City Morgue", "Juice WRLD", "Кишлак", "VA Kee"} {
		if placeholderArtist(name) {
			t.Errorf("%q was taken for a placeholder", name)
		}
	}
}

func TestAttemptsFallBackToTheAlbumArtist(t *testing.T) {
	// The file says the artist is VK, which is where it was downloaded from;
	// the real name is only in the album artist.
	got := attempts(Query{Artist: "VK", AlbumArtist: "City Morgue", Title: "GANG GREEN [Prod. Thraxx]"})
	if len(got) != 1 {
		t.Fatalf("expected one search, got %d: %+v", len(got), got)
	}
	if got[0].Artist != "City Morgue" {
		t.Errorf("searched as %q, expected City Morgue", got[0].Artist)
	}
	if got[0].Title != "GANG GREEN" {
		t.Errorf("searched for %q, expected GANG GREEN", got[0].Title)
	}
}

func TestAttemptsRetryWithoutTheQualifier(t *testing.T) {
	got := attempts(Query{Artist: "Juice WRLD", Title: "Deprived (Session Edit)"})
	if len(got) != 3 {
		t.Fatalf("expected three searches, got %d: %+v", len(got), got)
	}
	if got[0].Title != "Deprived (Session Edit)" {
		t.Errorf("first search was for %q", got[0].Title)
	}
	// The recording's own word goes in before the bare title.
	if got[1].Title != "Deprived session" {
		t.Errorf("second search was for %q, expected the title and its qualifier", got[1].Title)
	}
	if got[2].Title != "Deprived" {
		t.Errorf("third search was for %q, expected the bare title", got[2].Title)
	}
}

func TestAttemptsKeepOneSearchForAnOrdinaryTrack(t *testing.T) {
	got := attempts(Query{Artist: "Juice WRLD", AlbumArtist: "Juice WRLD", Title: "Lucid Dreams"})
	if len(got) != 1 {
		t.Errorf("an ordinary track took %d searches: %+v", len(got), got)
	}
}

func TestScoreAcceptsAnotherTakeOfTheSameSong(t *testing.T) {
	q := Query{Artist: "Juice WRLD", Title: "Deprived (Session Edit)"}

	session := Match{Artist: "Juice WRLD", Title: "Deprived (Studio Session)"}
	if got := score(q, session); got < 0.8 {
		t.Errorf("the same song in another take scored %.2f", got)
	}

	other := Match{Artist: "Juice WRLD", Title: "Wishing Well"}
	if score(q, other) >= score(q, session) {
		t.Error("a different song scored as high as another take of the right one")
	}
}

func TestWorthRetrying(t *testing.T) {
	for _, code := range []int{429, 500, 502, 503, 504} {
		if !worthRetrying(code) {
			t.Errorf("%d should be retried", code)
		}
	}
	for _, code := range []int{200, 301, 400, 404} {
		if worthRetrying(code) {
			t.Errorf("%d should not be retried", code)
		}
	}
}

func TestNameFromFile(t *testing.T) {
	cases := []struct{ path, artist, title string }{
		{"/sdcard/Music/VK/ZillaKami x SosMula - Bukkake.mp3", "ZillaKami x SosMula", "Bukkake"},
		{`D:\Music\04. Juice WRLD — Lucid Dreams.flac`, "Juice WRLD", "Lucid Dreams"},
		{"/sdcard/Music/Lucid Dreams.mp3", "", "Lucid Dreams"},
		{"", "", ""},
	}
	for _, c := range cases {
		artist, title := nameFromFile(c.path)
		if artist != c.artist || title != c.title {
			t.Errorf("nameFromFile(%q) = %q / %q, expected %q / %q", c.path, artist, title, c.artist, c.title)
		}
	}
}

func TestLeadArtistDropsTheUploader(t *testing.T) {
	cases := map[string]string{
		"Juice WRLD | @uploads_channel": "Juice WRLD",
		"@uploads_channel | Juice WRLD": "Juice WRLD",
		"Кино | t.me/rock":              "Кино",
		"@uploads_channel":              "@uploads_channel",
		"City Morgue":                   "City Morgue",
	}
	for in, want := range cases {
		if got := leadArtist(in); got != want {
			t.Errorf("leadArtist(%q) = %q, expected %q", in, got, want)
		}
	}
}

func TestSharedWordsCountAsAMatch(t *testing.T) {
	// The file names the two rappers; the database files the song under the
	// duo they record as, and credits one of them as a guest.
	q := Query{Artist: "ZillaKami x SosMula", Title: "Bukkake"}
	candidate := Match{Artist: "City Morgue (Ft. ZillaKami & SosMula)", Title: "Bukkake"}

	if got := score(q, candidate); got < 0.62 {
		t.Errorf("a credit sharing both names scored %.2f", got)
	}

	unrelated := Match{Artist: "Taylor Swift", Title: "Bukkake"}
	if score(q, unrelated) >= score(q, candidate) {
		t.Error("an unrelated artist scored as high as the one that shares both names")
	}
}

func TestAttemptsUseTheFileName(t *testing.T) {
	got := attempts(Query{
		Artist: "VK",
		Title:  "GANG GREEN",
		Path:   "/sdcard/Music/VK/ZillaKami x SosMula - GANG GREEN.mp3",
	})
	if len(got) == 0 {
		t.Fatal("no searches at all")
	}
	// The lead of the pair in the file name, the same reading every other
	// candidate gets.
	if got[0].Artist != "ZillaKami" {
		t.Errorf("searched as %q, expected the name from the file", got[0].Artist)
	}
}

func TestFromURLRefusesWhatItCannotRead(t *testing.T) {
	f := NewFinder()
	for _, link := range []string{"", "not a link", "ftp://genius.com/x", "https:///song"} {
		if _, err := f.FromURL(context.Background(), link); err == nil {
			t.Errorf("%q was accepted", link)
		}
	}
}

func TestTitleThatRepeatsTheArtist(t *testing.T) {
	got := attempts(Query{Artist: "Lil Uzi Vert", Title: "Lil Uzi Vert - No Script (Cannon)"})
	if len(got) == 0 {
		t.Fatal("no searches at all")
	}
	if got[0].Title != "No Script (Cannon)" {
		t.Errorf("searched for %q, expected the artist to be dropped from the title", got[0].Title)
	}
}

// "п.у." is what a Russian release writes for "feat.", and the word boundary
// in the pattern is an ASCII one, so it has to sit outside it.
func TestRussianGuestCreditIsDropped(t *testing.T) {
	cases := map[string]string{
		"Magic City п.у. @jetvillains": "Magic City",
		"Bankroll п.у. Sil-A":          "Bankroll",
		"Ангел и бес":                  "Ангел и бес",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, expected %q", in, got, want)
		}
	}
}

// A database files a collaboration under whoever it considers the lead, which
// is often the guest our title names.
func TestACandidateCreditedToTheGuestCounts(t *testing.T) {
	q := attempts(Query{Artist: "Juice WRLD", Title: "Boomerang (feat. Lil Yachty)"})[0]

	under := Match{Artist: "Lil Yachty", Title: "Boomerang"}
	if got := score(q, under); got < 0.62 {
		t.Errorf("a song filed under the named guest scored %.2f", got)
	}

	stranger := Match{Artist: "Taylor Swift", Title: "Boomerang"}
	if score(q, stranger) >= score(q, under) {
		t.Error("an unrelated artist scored as high as the named guest")
	}
}

func TestVersionMarkersComeOffTheBareTitle(t *testing.T) {
	cases := map[string]string{
		"Insecurities OG":   "Insecurities",
		"Push Me Away OG":   "Push Me Away",
		"Silent SHH (Stem)": "Silent SHH",
		"I Need More v2":    "I Need More",
		"Ride":              "Ride",
	}
	for in, want := range cases {
		if got := bareTitle(in); got != want {
			t.Errorf("bareTitle(%q) = %q, expected %q", in, got, want)
		}
	}
}

// What nearly matched is named rather than thrown away, since a collection of
// leaks is full of tracks the databases hold under another name.
func TestTheClosestMissIsReported(t *testing.T) {
	f := &Finder{MinScore: 0.62, rounds: [][]provider{{stubProvider{
		Match{Artist: "Some Alias", Title: "Ангел и Бес", Lyrics: "a line of a song\nand another line"},
	}}}}

	_, err := f.Find(context.Background(), Query{Artist: "ЛСП", Title: "Ангел и бес"})
	var missing *NotFound
	if !errors.As(err, &missing) {
		t.Fatalf("the near miss was not reported: %v", err)
	}
	if missing.Closest.Title != "Ангел и Бес" {
		t.Errorf("reported %q", missing.Closest.Title)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Error("the error no longer reads as not found")
	}
}

type stubProvider []Match

func (s stubProvider) name() string { return "stub" }
func (s stubProvider) search(context.Context, Query) ([]Match, error) {
	return []Match(s), nil
}

// A session edit is searched for as a session first, and the session's page
// wins over the released song's even though the released name is closer.
func TestSessionEditFindsTheSession(t *testing.T) {
	q := Query{Artist: "Juice WRLD", Title: "Everlasting Love V2 (Session Edit)"}

	if got := bareTitle(q.Title); got != "Everlasting Love" {
		t.Errorf("bareTitle = %q", got)
	}

	var titles []string
	for _, attempt := range attempts(q) {
		titles = append(titles, attempt.Title)
	}
	want := []string{"Everlasting Love V2 (Session Edit)", "Everlasting Love session", "Everlasting Love"}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Errorf("attempts = %q, expected %q", titles, want)
	}

	attempt := attempts(q)[2]
	released := score(attempt, Match{Artist: "Juice WRLD", Title: "Everlasting Love"})
	session := score(attempt, Match{Artist: "Juice WRLD", Title: "Everlasting Love (Sessions)"})
	if session <= released {
		t.Errorf("the session scored %.2f, the released song %.2f", session, released)
	}
}

// The right song in the wrong recording is only taken when the recording is
// nowhere to be found.
func TestReleasedSongIsOnlyAFallback(t *testing.T) {
	f := &Finder{MinScore: 0.62, rounds: [][]provider{{
		&titleStub{byTitle: map[string][]Match{
			"Everlasting Love V2 (Session Edit)": {{Artist: "Juice WRLD", Title: "Everlasting Love", Lyrics: strings.Repeat("released words ", 5)}},
			"Everlasting Love session":           {{Artist: "Juice WRLD", Title: "Everlasting Love (Sessions)", Lyrics: strings.Repeat("session words ", 5)}},
		}},
	}}}

	got, err := f.Find(context.Background(), Query{Artist: "Juice WRLD", Title: "Everlasting Love V2 (Session Edit)"})
	if err != nil || got.Title != "Everlasting Love (Sessions)" {
		t.Errorf("found %q, %v", got.Title, err)
	}

	f.rounds[0][0].(*titleStub).byTitle["Everlasting Love session"] = nil
	got, err = f.Find(context.Background(), Query{Artist: "Juice WRLD", Title: "Everlasting Love V2 (Session Edit)"})
	if err != nil || got.Title != "Everlasting Love" {
		t.Errorf("with no session anywhere, found %q, %v", got.Title, err)
	}
}

type titleStub struct{ byTitle map[string][]Match }

func (s *titleStub) name() string { return "stub" }

func (s *titleStub) search(_ context.Context, q Query) ([]Match, error) {
	return s.byTitle[q.Title], nil
}

// A file with no title in its tags is searched for by the name of the file.
func TestUntitledTrackIsSearchedByFileName(t *testing.T) {
	q := Query{Artist: "10age", Path: "/sdcard/Music/10age/10AGE - Близко.mp3"}
	if !Searchable(q) {
		t.Fatal("a track named in its file was left out")
	}
	got := attempts(q)
	if got[0].Title != "Близко" {
		t.Errorf("searched for %q, expected Близко", got[0].Title)
	}
	if DisplayTitle(q) != "Близко" {
		t.Errorf("DisplayTitle = %q", DisplayTitle(q))
	}

	// With no artist in the tags either, the file name supplies both.
	both := attempts(Query{Path: "/sdcard/Music/Music/XXXTENTACION - ILOVEITWHENTHEYRUN.mp3"})
	if len(both) == 0 || both[0].Artist != "XXXTENTACION" || both[0].Title != "ILOVEITWHENTHEYRUN" {
		t.Errorf("attempts = %+v", both)
	}

	if Searchable(Query{}) {
		t.Error("a track with nothing to go on was searchable")
	}
}

// A leak named after a line of the song is taken only when the line comes
// round as a hook, and only a name long enough to be a line is tried that way.
func TestHook(t *testing.T) {
	words := "You tryna fuck or sum'?\nYou tryna fuck or sum?\nI said you tryna fuck or sum'\nBaby, what you want?"
	if n := hookCount(words, "Fuck Or Sum"); n != 3 {
		t.Errorf("hookCount = %d, want 3", n)
	}
	if n := hookCount(words, "Baby"); n != 1 {
		t.Errorf("hookCount = %d, want 1", n)
	}
	for title, want := range map[string]bool{
		"Fuck Or Sum":                    true,
		"Fuck Or Sum (Session Edit)":     false, // the qualified attempt; the bare one follows
		"Love":                           false,
		"Go":                             false,
		"One Two Three Four Five Six Se": false,
	} {
		if got := hookAttempt(Query{Artist: "Juice WRLD", Title: title}); got != want {
			t.Errorf("hookAttempt(%q) = %v, want %v", title, got, want)
		}
	}
}
