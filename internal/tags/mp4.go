package tags

// MP4 tagging. Values live in moov/udta/meta/ilst as one box per field, each
// wrapping a `data` box that carries a type indicator and the payload.

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strconv"
	"time"
)

// iTunes atom names. The 0xA9 byte prefixes Apple's own fields.
const (
	atomTitle       = "\xa9nam"
	atomArtist      = "\xa9ART"
	atomAlbum       = "\xa9alb"
	atomYear        = "\xa9day"
	atomLyrics      = "\xa9lyr"
	atomGenre       = "\xa9gen"
	atomGenreID     = "gnre"
	atomAlbumArtist = "aART"
	atomCompilation = "cpil"
	atomSortAA      = "soaa"
	atomTrackNo     = "trkn"
	atomDiscNo      = "disk"
	atomFreeform    = "----"
)

// data box type indicators.
const (
	dataImplicit = 0
	dataUTF8     = 1
	dataJPEG     = 13
	dataPNG      = 14
)

// The boxes parsed into nodes; everything else is carried through untouched.
var mp4Descend = map[string]bool{"moov": true, "udta": true, "meta": true, "ilst": true}

func readMP4(path string) (Track, error) {
	var track Track

	f, err := os.Open(path)
	if err != nil {
		return track, wrap("opening MP4", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return track, wrap("reading MP4", err)
	}

	_, _, moovBuf, err := readMoov(f, info.Size())
	if err != nil {
		return track, err
	}

	roots, err := parseBoxes(moovBuf, mp4Descend)
	if err != nil || len(roots) == 0 {
		return track, wrap("parsing MP4", errBadBox)
	}
	moov := roots[0]

	track.TagBytes = int(len(moovBuf))
	track.Duration = mp4Duration(moov)

	ilst := findIlst(moov)
	if ilst == nil {
		return track, nil
	}

	for _, item := range ilst.kids {
		switch item.typ {
		case atomTitle:
			track.Title = atomText(item)
		case atomArtist:
			track.Artist = atomText(item)
		case atomAlbum:
			track.Album = atomText(item)
		case atomGenre:
			track.Genre = atomText(item)
		case atomAlbumArtist:
			track.AlbumArtist = atomText(item)
		case atomLyrics:
			track.Lyrics = atomText(item)
		case atomCompilation:
			track.Compilation = atomInt(item) != 0
		case atomSortAA:
			track.HasSort = true
		case atomTrackNo:
			track.TrackNo = atomPairFirst(item)
		case atomDiscNo:
			track.DiscNo = atomPairFirst(item)
		case atomYear:
			track.Year = parseYear(atomText(item))
		case atomFreeform:
			key, value := freeform(item)
			switch {
			case matchesAny(key, albumArtistKeys) && track.AlbumArtist == "":
				track.AlbumArtist = value
			case matchesAny(key, lyricsKeys) && track.Lyrics == "":
				track.Lyrics = value
			case matchesAny(key, sortKeys):
				track.HasSort = true
			case matchesAny(key, compilationKeys) && value != "" && value != "0":
				track.Compilation = true
			}
		}
	}

	return track, nil
}

func writeMP4(path string, edit Edit) error {
	f, err := os.Open(path)
	if err != nil {
		return wrap("opening MP4", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return wrap("reading MP4", err)
	}
	size := info.Size()

	moovAt, moovLen, moovBuf, err := readMoov(f, size)
	if err != nil {
		return err
	}

	roots, err := parseBoxes(moovBuf, mp4Descend)
	if err != nil || len(roots) == 0 {
		return wrap("parsing MP4", errBadBox)
	}
	moov := roots[0]

	ilst := ensureIlst(moov)
	applyMP4Edit(ilst, edit)
	if edit.ShrinkArtwork {
		shrinkMP4Artwork(ilst)
	}

	var rebuilt bytes.Buffer
	if err := moov.encode(&rebuilt); err != nil {
		return wrap("rebuilding MP4", err)
	}
	newMoov := rebuilt.Bytes()

	// mdat usually follows moov, so growing or shrinking moov moves every
	// sample. Offsets that already point before moov stay where they are.
	delta := int64(len(newMoov)) - moovLen
	if delta != 0 {
		if err := shiftChunkOffsets(newMoov, delta, moovAt+moovLen); err != nil {
			return wrap("adjusting MP4 chunk offsets", err)
		}
	}

	tmp := path + ".mlm-tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return wrap("writing MP4", err)
	}

	err = writeSections(out, f, moovAt, moovLen, size, newMoov)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return wrap("writing MP4", err)
	}

	// Windows refuses to replace a file that is still open.
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return wrap("writing MP4", err)
	}

	return replaceFile(tmp, path)
}

// writeSections streams the file back out with moov replaced in place.
func writeSections(out io.Writer, src io.ReaderAt, moovAt, moovLen, size int64, newMoov []byte) error {
	if _, err := io.Copy(out, io.NewSectionReader(src, 0, moovAt)); err != nil {
		return err
	}
	if _, err := out.Write(newMoov); err != nil {
		return err
	}
	tailAt := moovAt + moovLen
	_, err := io.Copy(out, io.NewSectionReader(src, tailAt, size-tailAt))
	return err
}

func applyMP4Edit(ilst *node, edit Edit) {
	for _, field := range []struct {
		atom  string
		value *string
	}{
		{atomArtist, edit.Artist},
		{atomAlbum, edit.Album},
		{atomTitle, edit.Title},
		{atomGenre, edit.Genre},
	} {
		if field.value != nil {
			setTextAtom(ilst, field.atom, *field.value)
		}
	}
	if edit.Genre != nil {
		// The numeric genre is a relic that would otherwise override the text
		// one in some players.
		ilst.removeChild(atomGenreID)
	}
	if edit.Year != nil {
		setTextAtom(ilst, atomYear, formatNumber(*edit.Year))
	}
	for _, pair := range []struct {
		atom  string
		value *int
	}{
		{atomTrackNo, edit.TrackNo},
		{atomDiscNo, edit.DiscNo},
	} {
		if pair.value != nil {
			setPairAtom(ilst, pair.atom, *pair.value)
		}
	}
	if edit.AlbumArtist != nil {
		setTextAtom(ilst, atomAlbumArtist, *edit.AlbumArtist)
		if *edit.AlbumArtist == "" {
			removeFreeform(ilst, albumArtistKeys)
		}
	}
	if edit.Lyrics != nil {
		setTextAtom(ilst, atomLyrics, *edit.Lyrics)
		if *edit.Lyrics == "" {
			removeFreeform(ilst, lyricsKeys)
		}
	}
	if edit.RemoveCompilation {
		ilst.removeChild(atomCompilation)
		removeFreeform(ilst, compilationKeys)
	}
	if edit.RemoveAlbumArtistSort {
		ilst.removeChild(atomSortAA)
		removeFreeform(ilst, sortKeys)
	}
}

func setTextAtom(ilst *node, typ, value string) {
	if value == "" {
		ilst.removeChild(typ)
		return
	}
	ilst.replaceChild(textAtom(typ, value))
}

// setPairAtom writes a trkn or disk atom, which stores the number and the
// total as a run of 16-bit fields. The total already on the file is kept.
func setPairAtom(ilst *node, typ string, number int) {
	if number <= 0 {
		ilst.removeChild(typ)
		return
	}

	total := 0
	if existing := ilst.child(typ); existing != nil {
		if payload, _, ok := atomData(existing.body); ok && len(payload) >= 6 {
			total = int(binary.BigEndian.Uint16(payload[4:6]))
		}
	}

	payload := make([]byte, 8)
	binary.BigEndian.PutUint16(payload[2:4], uint16(number))
	binary.BigEndian.PutUint16(payload[4:6], uint16(total))

	body := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(body[0:4], uint32(len(body)))
	copy(body[4:8], "data")
	// Track and disc numbers are stored as raw bytes, not as text.
	binary.BigEndian.PutUint32(body[8:12], dataImplicit)
	copy(body[16:], payload)

	ilst.replaceChild(newLeaf(typ, body))
}

func removeFreeform(ilst *node, keys []string) {
	kept := ilst.kids[:0]
	for _, kid := range ilst.kids {
		if kid.typ == atomFreeform {
			if key, _ := freeform(kid); matchesAny(key, keys) {
				continue
			}
		}
		kept = append(kept, kid)
	}
	ilst.kids = kept
}

// readMoov locates the moov box among the top-level boxes and loads it. Only
// moov is held in memory; sample data is streamed when the file is rewritten.
func readMoov(r io.ReaderAt, size int64) (at, length int64, buf []byte, err error) {
	var head [16]byte

	for off := int64(0); off+8 <= size; {
		if _, err := r.ReadAt(head[:8], off); err != nil {
			return 0, 0, nil, wrap("reading MP4", err)
		}

		boxLen := int64(binary.BigEndian.Uint32(head[:4]))
		typ := string(head[4:8])
		switch boxLen {
		case 0:
			boxLen = size - off
		case 1:
			if _, err := r.ReadAt(head[8:], off+8); err != nil {
				return 0, 0, nil, wrap("reading MP4", err)
			}
			boxLen = int64(binary.BigEndian.Uint64(head[8:]))
		}
		if boxLen < 8 || boxLen > size-off { // no off+boxLen: a 64-bit length can overflow it
			if off == 0 {
				// Nothing at the very start of the file is a box at all, which
				// is what an unfinished download looks like: the first part of
				// the file is still zeros.
				return 0, 0, nil, wrap("reading MP4", errNoHeader)
			}
			return 0, 0, nil, wrap("reading MP4", errBadBox)
		}

		if typ == "moov" {
			buf = make([]byte, boxLen)
			if _, err := r.ReadAt(buf, off); err != nil {
				return 0, 0, nil, wrap("reading MP4", err)
			}
			return off, boxLen, buf, nil
		}
		off += boxLen
	}

	// Every box was walked and none of them held the metadata.
	return 0, 0, nil, wrap("reading MP4", errNoHeader)
}

func findIlst(moov *node) *node {
	// The common layout is moov/udta/meta/ilst; QuickTime puts meta directly
	// under moov.
	for _, parent := range []*node{moov.child("udta"), moov} {
		if parent == nil {
			continue
		}
		if meta := parent.child("meta"); meta != nil {
			if ilst := meta.child("ilst"); ilst != nil {
				return ilst
			}
		}
	}
	return nil
}

// ensureIlst returns the item list, creating the udta/meta/ilst chain when the
// file carries no metadata at all.
func ensureIlst(moov *node) *node {
	if ilst := findIlst(moov); ilst != nil {
		return ilst
	}

	udta := moov.child("udta")
	if udta == nil {
		udta = newContainer("udta", false)
		moov.kids = append(moov.kids, udta)
	}
	meta := udta.child("meta")
	if meta == nil {
		meta = newContainer("meta", true, metadataHandler())
		udta.kids = append(udta.kids, meta)
	}
	ilst := meta.child("ilst")
	if ilst == nil {
		ilst = newContainer("ilst", false)
		meta.kids = append(meta.kids, ilst)
	}
	return ilst
}

// metadataHandler builds the hdlr box that marks a meta box as holding iTunes
// metadata. Players ignore an ilst that is not announced this way.
func metadataHandler() *node {
	body := make([]byte, 25)
	copy(body[8:12], "mdir")
	copy(body[12:16], "appl")
	return newLeaf("hdlr", body)
}

// mp4Duration reads the movie header, whose timescale and duration give the
// playing time used to match lyrics.
func mp4Duration(moov *node) time.Duration {
	mvhd := moov.child("mvhd")
	if mvhd == nil || len(mvhd.body) < 20 {
		return 0
	}

	body := mvhd.body
	var timescale, units uint64
	if body[0] == 1 {
		if len(body) < 32 {
			return 0
		}
		timescale = uint64(binary.BigEndian.Uint32(body[20:24]))
		units = binary.BigEndian.Uint64(body[24:32])
	} else {
		timescale = uint64(binary.BigEndian.Uint32(body[12:16]))
		units = uint64(binary.BigEndian.Uint32(body[16:20]))
	}
	if timescale == 0 {
		return 0
	}
	return time.Duration(units) * time.Second / time.Duration(timescale)
}

// textAtom builds an ilst item holding a UTF-8 string.
func textAtom(typ, value string) *node {
	return newLeaf(typ, dataBox(dataUTF8, []byte(value)))
}

// dataBox wraps a value in the `data` box every ilst item carries.
func dataBox(kind uint32, payload []byte) []byte {
	body := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(body[0:4], uint32(len(body)))
	copy(body[4:8], "data")
	binary.BigEndian.PutUint32(body[8:12], kind)
	copy(body[16:], payload)
	return body
}

// atomData unwraps the `data` box inside an ilst item.
func atomData(body []byte) (payload []byte, kind uint32, ok bool) {
	if len(body) < 16 {
		return nil, 0, false
	}
	size := int(binary.BigEndian.Uint32(body[0:4]))
	if string(body[4:8]) != "data" || size < 16 || size > len(body) {
		return nil, 0, false
	}
	return body[16:size], binary.BigEndian.Uint32(body[8:12]), true
}

func atomText(n *node) string {
	payload, kind, ok := atomData(n.body)
	if !ok || (kind != dataUTF8 && kind != dataImplicit) {
		return ""
	}
	return cleanText(string(payload))
}

func atomInt(n *node) int {
	payload, _, ok := atomData(n.body)
	if !ok {
		return 0
	}
	value := 0
	for _, b := range payload {
		value = value<<8 | int(b)
	}
	return value
}

// atomPairFirst reads the leading number of a trkn/disk pair, which stores
// "number of total" as a run of 16-bit fields.
func atomPairFirst(n *node) int {
	payload, _, ok := atomData(n.body)
	if !ok || len(payload) < 4 {
		return 0
	}
	return int(binary.BigEndian.Uint16(payload[2:4]))
}

// freeform reads the key and value of a "----" atom, which names its own field
// through `mean` and `name` boxes.
func freeform(n *node) (key, value string) {
	kids, err := parseBoxes(n.body, nil)
	if err != nil {
		return "", ""
	}
	for _, kid := range kids {
		switch kid.typ {
		case "name":
			if len(kid.body) > 4 {
				key = string(kid.body[4:]) // Skip version and flags.
			}
		case "data":
			// Here the data box is a sibling rather than a wrapper, so it is
			// re-encoded to reuse the same unwrapping logic.
			var wrapped bytes.Buffer
			if err := kid.encode(&wrapped); err != nil {
				continue
			}
			if payload, kind, ok := atomData(wrapped.Bytes()); ok && (kind == dataUTF8 || kind == dataImplicit) {
				value = string(payload)
			}
		}
	}
	return key, value
}

func parseYear(s string) int {
	if len(s) >= 4 {
		if year, err := strconv.Atoi(s[:4]); err == nil {
			return year
		}
	}
	return 0
}
