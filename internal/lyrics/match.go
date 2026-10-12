package lyrics

// Deciding whether a search result really is the track in hand. Filenames and
// tags in a real collection carry a lot of noise — "(Official Audio)",
// "[Prod. Nick Mira]", "feat." credits — that a lyrics database does not, so
// both sides are cleaned before they are compared.

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Bracketed asides that describe the recording rather than name the song.
var noiseChunk = regexp.MustCompile(`(?i)[\(\[]\s*(?:feat|ft|featuring|with|prod|produced)\b[^\)\]]*[\)\]]|` +
	`[\(\[]\s*(?:official\b[^\)\]]*|lyrics?|audio|visuali[sz]er|music video|video|hq|hd|explicit|clean|dirty|cdq|full|snippet|leak(?:ed)?|unreleased|og|master(?:ed)?|remaster(?:ed)?[^\)\]]*)\s*[\)\]]`)

// Trailing "- Remastered 2011" style qualifiers.
var noiseSuffix = regexp.MustCompile(`(?i)\s+-\s+(?:remaster(?:ed)?|single version|album version|radio edit|bonus track|deluxe)\b.*$`)

// A leading track number, as ripped filenames often carry.
var leadingNumber = regexp.MustCompile(`^\s*\d{1,3}\s*[-.)]\s+`)

var punctuation = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// cleanTitle strips the decoration around a song title.
func cleanTitle(title string) string {
	title = strings.TrimSpace(title)
	title = leadingNumber.ReplaceAllString(title, "")
	title = noiseChunk.ReplaceAllString(title, " ")
	title = noiseSuffix.ReplaceAllString(title, "")

	// A trailing "feat. …" with no brackets around it.
	if loc := guestSuffix.FindStringIndex(title); loc != nil && loc[0] > 0 {
		title = title[:loc[0]]
	}
	// And a producer's credit: "U Okay Prod. by Eskiimo".
	if loc := producerSuffix.FindStringIndex(title); loc != nil && loc[0] > 0 {
		title = title[:loc[0]]
	}
	return strings.TrimSpace(strings.Join(strings.Fields(title), " "))
}

// A guest credit, in either alphabet: "feat.", "ft", and the "п.у." a Russian
// release writes for the same thing.
//
// The Russian spellings are kept out of the \b guard, because a word boundary
// here is an ASCII one: it never matches beside a Cyrillic letter, and the
// credit would stay in the name that goes to the databases.
// A producer credited after the title with no brackets around it.
var producerSuffix = regexp.MustCompile(`(?i)\s+[-–—|]?\s*(?:prod\.\s*(?:by\s+)?|prod\s+by\s+|produced\s+by\s+)\S.*$`)

var guestSuffix = regexp.MustCompile(`(?i)\s+(?:\b(?:feat|ft|featuring|w/)\b\.?|п\.\s?у\.?|при участии|совместно с)\s+`)

// The same credit as a title carries it, where a bracket usually holds it:
// "Boomerang (feat. Lil Yachty)".
var guestCredit = regexp.MustCompile(`(?i)[\(\[]?\s*(?:\b(?:feat|ft|featuring|with)\b\.?|п\.\s?у\.?|при участии)\s+`)

// Anything left inside brackets, and the markers an unreleased recording
// carries at the end of its name.
var (
	bracketedAside = regexp.MustCompile(`[\(\[][^\)\]]*[\)\]]`)
	versionSuffix  = regexp.MustCompile(`(?i)\s+(?:v\.?\d+|og|stem|stems|solo|snippet|master|cdq)$`)
)

// bareTitle is the song's name with every qualifier taken off, including the
// ones cleanTitle leaves alone because they can belong to a title.
//
// A collection of unreleased music is full of them — "Deprived (Session Edit)",
// "Devil Horns (v1)" — and a database that has the song files it under the
// plain name, so searching for the decorated one finds nothing at all.
func bareTitle(title string) string {
	// The spaces are closed up before the version marker is looked for at the
	// end: a bracket taken off leaves them there, and "Everlasting Love V2
	// (Session Edit)" kept its "V2" and found nothing.
	bare := bracketedAside.ReplaceAllString(cleanTitle(title), " ")
	bare = strings.Join(strings.Fields(bare), " ")
	bare = versionSuffix.ReplaceAllString(bare, "")
	bare = strings.TrimSpace(strings.Join(strings.Fields(bare), " "))
	if bare == "" {
		return cleanTitle(title) // The whole title was inside brackets.
	}
	return bare
}

// Words in a title's brackets that only describe the file, not which
// recording it is: an edit, a version number, where it leaked from.
var fileQualifiers = map[string]bool{
	"edit": true, "edited": true, "version": true, "ver": true, "cdq": true, "hq": true,
	"lq": true, "snippet": true, "leak": true, "leaked": true, "unreleased": true,
	"master": true, "mastered": true, "full": true, "clean": true, "explicit": true,
	"dirty": true, "audio": true, "official": true, "lyric": true, "lyrics": true,
	"video": true, "stem": true, "stems": true, "the": true, "and": true,
}

var versionWord = regexp.MustCompile(`^v\d+$`)

// qualifiers are the words in a title's brackets that name the recording:
// "session" in "Everlasting Love V2 (Session Edit)", "live" and "acoustic"
// elsewhere. A guest credit or a producer is not one, and neither is anything
// in fileQualifiers.
func qualifiers(title string) []string {
	var out []string
	seen := map[string]bool{}
	for _, aside := range bracketedAside.FindAllString(cleanTitle(title), -1) {
		for _, word := range words(aside) {
			word = qualifierStem(word)
			if fileQualifiers[word] || versionWord.MatchString(word) || seen[word] {
				continue
			}
			seen[word] = true
			out = append(out, word)
		}
	}
	return out
}

// qualifierStem folds "sessions" and "session" together.
func qualifierStem(word string) string {
	if len([]rune(word)) > 4 {
		return strings.TrimSuffix(word, "s")
	}
	return word
}

// hasQualifiers says whether a candidate's title names the recording the
// query's qualifiers describe. A query without any is matched by anything.
func hasQualifiers(want []string, title string) bool {
	if len(want) == 0 {
		return true
	}
	have := map[string]bool{}
	for _, word := range words(title) {
		have[qualifierStem(word)] = true
	}
	for _, word := range want {
		if !have[word] {
			return false
		}
	}
	return true
}

// Names that are not artists. A downloader writes where the file came from
// into the artist field, and a compilation with nobody credited gets a
// stand-in; either way the real name is in the album artist instead.
var placeholderNames = map[string]bool{
	"vk": true, "vkcom": true, "vkmusic": true, "vkmp3": true, "vkmusicru": true,
	"unknown": true, "unknownartist": true, "noartist": true, "none": true,
	"various": true, "variousartists": true, "va": true,
	"youtube": true, "soundcloud": true, "spotify": true, "telegram": true,
	"mp3": true, "audio": true, "music": true,
	"музыка": true, "сборник": true, "неизвестен": true, "неизвестный": true,
	"неизвестныйисполнитель": true,
}

func placeholderArtist(name string) bool { return placeholderNames[fold(name)] }

// leadArtist drops the guests, since databases file a song under its lead.
func leadArtist(artist string) string {
	artist = dropWatermark(artist)

	if loc := guestSuffix.FindStringIndex(artist); loc != nil && loc[0] > 0 {
		return strings.TrimSpace(artist[:loc[0]])
	}
	for _, sep := range []string{"|", ";", " & ", " / ", " x ", ", "} {
		if before, _, found := strings.Cut(artist, sep); found {
			if before = strings.TrimSpace(before); before != "" {
				return before
			}
		}
	}
	return strings.TrimSpace(artist)
}

// Whoever shared the file, written into the artist field beside the artist.
var handle = regexp.MustCompile(`(?i)\s*[|,;]?\s*(?:@\S+|(?:https?://)?(?:www\.)?(?:t\.me|vk\.com|telegram\.me)/\S*)\s*`)

// dropWatermark takes the re-uploader's name back out of a credit, so that
// "Juice WRLD | @uploads_channel" is searched for as Juice WRLD.
func dropWatermark(artist string) string {
	// Taking the handle out can leave the separator that held it in place.
	stripped := strings.Trim(handle.ReplaceAllString(artist, " "), " |,;-")
	if stripped == "" {
		return strings.TrimSpace(artist) // It was nothing but a handle.
	}
	return strings.Join(strings.Fields(stripped), " ")
}

func fold(s string) string {
	// Russian prints "ё" as "е" as often as not: "Солнце взойдет" is Genius's
	// "Солнце взойдёт".
	s = strings.NewReplacer("ё", "е", "Ё", "е").Replace(strings.ToLower(strings.TrimSpace(s)))
	return punctuation.ReplaceAllString(s, "")
}

// score rates how well a candidate matches the query, from 0 to 1.
func score(q Query, candidate Match) float64 {
	title := similarity(cleanTitle(q.Title), cleanTitle(candidate.Title))

	// "Deprived (Session Edit)" and "Deprived (Studio Session)" are the same
	// song recorded twice, so the names are compared without their qualifiers
	// as well — a shade below the full comparison, so a candidate whose name
	// really is the same still wins.
	if bare := 0.95 * similarity(bareTitle(q.Title), bareTitle(candidate.Title)); bare > title {
		title = bare
	}

	// The tag may credit guests the database leaves out, or the other way
	// round, so the lead artist is compared too and the better reading counts.
	artist := similarity(q.Artist, candidate.Artist)
	if lead := similarity(leadArtist(q.Artist), leadArtist(candidate.Artist)); lead > artist {
		artist = lead
	}

	// Neither name has to be written the same way on both sides. A file called
	// "ZillaKami x SosMula - Bukkake" and a database entry for "City Morgue
	// (Ft. ZillaKami)" share the names that matter and nothing else, and
	// letter-by-letter they look unrelated, so the words they have in common
	// count too — a little below a real match, since sharing some words is
	// weaker evidence than being the same name.
	if shared := 0.9 * wordOverlap(q.Artist, candidate.Artist); shared > artist {
		artist = shared
	}

	// A collaboration is filed under whoever the database considers the lead,
	// which is often the guest named in our title: "Act Right (feat. GDo)" is
	// GDo's song there. The guest is on the track either way, so a candidate
	// credited to them is credited to somebody who was in the room.
	for _, guest := range q.Guests {
		named := 0.95 * similarity(guest, candidate.Artist)
		if shared := 0.9 * wordOverlap(guest, candidate.Artist); shared > named {
			named = shared
		}
		if named > artist {
			artist = named
		}
	}
	if shared := 0.9 * wordOverlap(q.Title, candidate.Title); shared > title {
		title = shared
	}
	// On the same album under the same name: the source checked that, and it
	// is as good as the artist's name — a joint album files a song under one
	// of the two artists, and the tag often names the other.
	if candidate.SameAlbum && artist < sameAlbumArtistScore {
		artist = sameAlbumArtistScore
	}
	// Named after its hook: the source already checked that the words repeat
	// the title, which is as good as a close name.
	if candidate.Hook && title < hookTitleScore {
		title = hookTitleScore
	}

	total := 0.6*title + 0.4*artist

	// The right song in the wrong recording is a weaker match than the right
	// recording, whichever name happens to be spelled closer.
	if len(q.Qualifiers) > 0 {
		if hasQualifiers(q.Qualifiers, candidate.Title) {
			total += 0.04
		} else {
			total -= 0.08
		}
	}

	// Two songs with the same name by the same artist are told apart by how
	// long they run.
	if q.Duration > 0 && candidate.Duration > 0 {
		gap := q.Duration - candidate.Duration
		if gap < 0 {
			gap = -gap
		}
		if gap <= 3*time.Second {
			total += 0.06
		} else if gap > 20*time.Second {
			total -= 0.15
		}
	}

	return clamp(total)
}

// withoutLeadingArtist drops the "Artist - " that a title repeats.
//
// A file named "Artist - Title.mp3" is often tagged with the whole of that as
// its title, and searching for the artist twice finds nothing at all.
func withoutLeadingArtist(title string, artists []string) string {
	for _, artist := range artists {
		if artist == "" {
			continue
		}
		// A file name often runs the two together: "семьсот семь-Пропущенный".
		// Only a name of some length is taken off that way, or "Jay-Z" by Jay
		// would be searched for as "Z".
		separators := []string{" - ", " — ", " – ", " -- "}
		if utf8.RuneCountInString(artist) >= 5 {
			separators = append(separators, "-", "–", "—")
		}
		// "Juice WRLD Tell Me U Luv Me", read off a file name written with
		// underscores: a name of two words or more is taken off a plain space.
		if len(strings.Fields(artist)) >= 2 {
			separators = append(separators, " ")
		}
		for _, separator := range separators {
			prefix := artist + separator
			if len(title) > len(prefix) && strings.EqualFold(title[:len(prefix)], prefix) {
				return strings.TrimSpace(title[len(prefix):])
			}
		}
	}
	return title
}

// guestsIn reads the names credited after a "feat." in a title.
func guestsIn(title string) []string {
	loc := guestCredit.FindStringIndex(title)
	if loc == nil {
		return nil
	}

	// The credit runs to the end of the title, or to the bracket that closed
	// around it.
	tail := title[loc[1]:]
	if end := strings.IndexAny(tail, ")]"); end >= 0 {
		tail = tail[:end]
	}

	var guests []string
	for _, name := range guestSeparator.Split(tail, -1) {
		if name = strings.TrimSpace(name); len(name) > 2 {
			guests = append(guests, name)
		}
	}
	return guests
}

var guestSeparator = regexp.MustCompile(`\s*(?:&|,|;|\+|\band\b|\bи\b)\s*`)

// wordOverlap is the share of the shorter side's words that also appear on the
// other, so a name that carries extra words is still recognised by the ones it
// has. Short words are ignored: "the" and "x" are in half the names there are.
func wordOverlap(a, b string) float64 {
	left, right := words(a), words(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	if len(left) > len(right) {
		left, right = right, left
	}

	have := make(map[string]bool, len(right))
	for _, word := range right {
		have[word] = true
	}

	shared := 0
	for _, word := range left {
		if have[word] {
			shared++
		}
	}
	return float64(shared) / float64(len(left))
}

func words(s string) []string {
	var out []string
	for _, field := range punctuation.Split(strings.ToLower(strings.TrimSpace(s)), -1) {
		if len([]rune(field)) > 2 {
			out = append(out, field)
		}
	}
	return out
}

func clamp(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// similarity compares two strings once their case and punctuation are folded
// away, scoring an exact match highest and edit distance otherwise.
func similarity(a, b string) float64 {
	a, b = fold(a), fold(b)
	switch {
	case a == "" || b == "":
		return 0
	case a == b:
		return 1
	case strings.Contains(a, b) || strings.Contains(b, a):
		return 0.9
	}

	longest := max(len(a), len(b))
	return clamp(1 - float64(levenshtein(a, b))/float64(longest))
}

// levenshtein is the usual two-row edit distance, over bytes: both inputs are
// already folded, so the difference between bytes and runes does not change
// which candidate wins.
func levenshtein(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}

	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(current[j-1]+1, previous[j]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}

var blankRun = regexp.MustCompile(`\n{3,}`)

// tidyLyrics normalizes line endings and rejects the placeholders databases
// return in place of real words.
func tidyLyrics(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	text = blankRun.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	text = strings.TrimSpace(text)

	switch strings.ToLower(text) {
	case "", "[instrumental]", "instrumental", "(instrumental)":
		return ""
	}
	// Anything this short is a stub rather than a song.
	if len([]rune(text)) < 25 {
		return ""
	}
	return text
}

// A song found by its hook is worth this much of a title match, and the hook
// has to come round this often: a line said once is not what a song is named
// after.
const (
	sameAlbumArtistScore = 0.85
	hookTitleScore       = 0.85
	defaultMinScore      = 0.62
	minHookRepeats       = 3
)

// hookAttempt says whether a search is worth repeating by the title alone:
// once per track, on the bare name, and only for a name long enough to be a
// line rather than a word half the songs there are contain.
func hookAttempt(q Query) bool {
	bare := bareTitle(q.Title)
	if fold(bare) != fold(q.Title) || (len(q.Qualifiers) > 0 && hasQualifiers(q.Qualifiers, q.Title)) {
		return false
	}
	n := len(strings.Fields(bare))
	return n >= 2 && n <= 6 && len(fold(bare)) >= 8
}

func anyAccepted(q Query, matches []Match) bool {
	for _, m := range matches {
		if score(q, m) >= defaultMinScore {
			return true
		}
	}
	return false
}

// hookCount is how many times a phrase is sung in the lyrics, word for word.
func hookCount(lyrics, phrase string) int {
	norm := func(s string) string {
		return " " + strings.Join(strings.Fields(punctuation.ReplaceAllString(strings.ToLower(s), " ")), " ") + " "
	}
	p := norm(phrase)
	if strings.TrimSpace(p) == "" {
		return 0
	}
	return strings.Count(norm(lyrics), p)
}

// translation is a bracketed name in the Latin alphabet at the end of a name
// in another one: Genius writes "Пропущенный (Missed)" and "семьсот семь
// (sem’sot sem’)".
var translation = regexp.MustCompile(`\s*[(\[]([^()\[\]]*)[)\]]\s*$`)

// withoutTranslation drops the translations a database adds to names written
// in another alphabet, so that they compare with the names in the tags.
func withoutTranslation(name string) string {
	for {
		loc := translation.FindStringSubmatchIndex(name)
		if loc == nil {
			return name
		}
		rest, inside := name[:loc[0]], name[loc[2]:loc[3]]
		if !hasNonLatin(rest) || hasNonLatin(inside) {
			return name
		}
		name = strings.TrimSpace(rest)
	}
}

func hasNonLatin(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) && !unicode.Is(unicode.Latin, r) {
			return true
		}
	}
	return false
}

// otherArtists are the names after the first in a credit: "Кишлак" in
// "семьсот семь, Кишлак". The song may be filed under any of them.
func otherArtists(credit string) []string {
	credit = dropWatermark(credit)
	var names []string
	for _, sep := range []string{"|", ";", " & ", " / ", " x ", ", "} {
		parts := strings.Split(credit, sep)
		for _, part := range parts[1:] {
			if part = strings.TrimSpace(part); part != "" {
				names = append(names, part)
			}
		}
	}
	return names
}

// recordingWords say which recording a bracket is about rather than naming
// the song: "(Session Edit)", "(Remix)", "(Live)".
var recordingWords = map[string]bool{
	"session": true, "sessions": true, "studio": true, "remix": true, "mix": true, "live": true,
	"acoustic": true, "demo": true, "og": true, "original": true, "alt": true, "alternate": true,
	"extended": true, "slowed": true, "reverb": true, "sped": true, "instrumental": true,
	"remaster": true, "remastered": true, "take": true, "intro": true, "outro": true,
	"interlude": true, "skit": true, "bonus": true, "deluxe": true, "radio": true,
	"solo": true, "acapella": true, "capella": true, "cover": true, "reprise": true,
	"unplugged": true, "mono": true, "stereo": true, "short": true, "long": true,
	"finished": true, "unfinished": true, "verse": true, "hook": true, "chorus": true,
	"part": true, "pt": true, "vip": true, "rework": true, "bootleg": true, "mashup": true,
	"chopped": true, "screwed": true, "nightcore": true, "speed": true, "instr": true,
}

// aliases are the other names a title gives its song in brackets: a leak is
// often known by two, as "Double R's (Dark Tints)" is Genius's "Double R (Dark
// Tints)" and turns up only under the second. A bracket that says which
// recording this is, or holds a year, is no name.
func aliases(title string) []string {
	var out []string
	// "Around Me x Clout": two songs cut together, each searched for by its
	// own name.
	if parts := strings.Split(bareTitle(title), " x "); len(parts) > 1 {
		for _, part := range parts {
			if part = strings.TrimSpace(part); len([]rune(part)) >= 3 {
				out = append(out, part)
			}
		}
	}
	for _, aside := range bracketedAside.FindAllString(cleanTitle(title), -1) {
		inner := strings.TrimSpace(aside[1 : len(aside)-1])
		ws := words(inner)
		if len(ws) == 0 {
			continue
		}
		name := true
		letters := false
		for _, word := range ws {
			if fileQualifiers[word] || recordingWords[word] || versionWord.MatchString(word) {
				name = false
				break
			}
			for _, r := range word {
				letters = letters || unicode.IsLetter(r)
			}
		}
		if name && letters {
			out = append(out, inner)
		}
	}
	return out
}
