package tags

// MP4 metadata lives in moov/udta/meta/ilst. Editing it changes the size of
// moov, which shifts mdat and invalidates every chunk offset recorded in
// stco/co64 — so a rewrite has to patch those tables by the same delta.
//
// Only the path down to ilst is parsed into nodes; every other box is carried
// through as opaque bytes, which keeps unknown boxes byte-identical.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var errBadBox = errors.New("malformed MP4 structure")

// errNoHeader marks a file with no readable MP4 header, which is what a
// download that never finished leaves behind: audio somewhere in the middle
// and zeros where the header should be. Nothing can be read from or written to
// such a file, and no player will open it either.
var errNoHeader = errors.New("the file has no MP4 header — it looks damaged")

// Boxes that hold other boxes. Only these are descended into when patching
// chunk offset tables; anything else is a leaf.
var containerBoxes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "edts": true, "udta": true, "mvex": true,
}

// node is a parsed box. A node either carries raw bytes (leaf) or children.
type node struct {
	typ   string
	large bool    // Size was stored as a 64-bit value.
	full  bool    // Four version/flags bytes precede the children (meta).
	body  []byte  // Leaf contents, excluding the header.
	kids  []*node // Child boxes, for containers.
	leaf  bool
}

func newLeaf(typ string, body []byte) *node {
	return &node{typ: typ, body: body, leaf: true}
}

func newContainer(typ string, full bool, kids ...*node) *node {
	return &node{typ: typ, full: full, kids: kids}
}

// size returns the encoded length of the box, header included.
func (n *node) size() int64 {
	inner := int64(0)
	if n.leaf {
		inner = int64(len(n.body))
	} else {
		if n.full {
			inner += 4
		}
		for _, kid := range n.kids {
			inner += kid.size()
		}
	}

	header := int64(8)
	if n.large {
		header = 16
	}
	return header + inner
}

func (n *node) encode(w io.Writer) error {
	size := n.size()

	head := make([]byte, 8, 16)
	if n.large {
		binary.BigEndian.PutUint32(head[:4], 1)
		copy(head[4:8], n.typ)
		head = head[:16]
		binary.BigEndian.PutUint64(head[8:16], uint64(size))
	} else {
		if size > 0xFFFFFFFF {
			return fmt.Errorf("%w: box %s does not fit in 32 bits", errBadBox, n.typ)
		}
		binary.BigEndian.PutUint32(head[:4], uint32(size))
		copy(head[4:8], n.typ)
	}
	if _, err := w.Write(head); err != nil {
		return err
	}

	if n.leaf {
		_, err := w.Write(n.body)
		return err
	}
	if n.full {
		if _, err := w.Write([]byte{0, 0, 0, 0}); err != nil {
			return err
		}
	}
	for _, kid := range n.kids {
		if err := kid.encode(w); err != nil {
			return err
		}
	}
	return nil
}

func (n *node) child(typ string) *node {
	for _, kid := range n.kids {
		if kid.typ == typ {
			return kid
		}
	}
	return nil
}

func (n *node) removeChild(typ string) {
	kept := n.kids[:0]
	for _, kid := range n.kids {
		if kid.typ != typ {
			kept = append(kept, kid)
		}
	}
	n.kids = kept
}

// replaceChild swaps an existing child of the same type, or appends it.
func (n *node) replaceChild(child *node) {
	for i, kid := range n.kids {
		if kid.typ == child.typ {
			n.kids[i] = child
			return
		}
	}
	n.kids = append(n.kids, child)
}

// parseBoxes splits a run of sibling boxes. Boxes on the path towards ilst are
// parsed recursively; everything else is kept as an opaque leaf.
func parseBoxes(buf []byte, descend map[string]bool) ([]*node, error) {
	var out []*node

	for off := 0; off+8 <= len(buf); {
		size := int64(binary.BigEndian.Uint32(buf[off : off+4]))
		typ := string(buf[off+4 : off+8])
		header := int64(8)
		large := false

		switch size {
		case 0:
			size = int64(len(buf) - off) // Runs to the end of the parent.
		case 1:
			if off+16 > len(buf) {
				return nil, errBadBox
			}
			size = int64(binary.BigEndian.Uint64(buf[off+8 : off+16]))
			header, large = 16, true
		}
		if size < header || size > int64(len(buf)-off) { // no off+size: a 64-bit size can overflow it
			return nil, errBadBox
		}

		body := buf[off+int(header) : off+int(size)]
		n := &node{typ: typ, large: large}

		switch {
		case !descend[typ]:
			n.leaf, n.body = true, body
		case typ == "meta":
			// `meta` is a full box, but a few encoders write it as a plain
			// container; whichever layout parses wins.
			kids, err := parseBoxes(skipVersionFlags(body), descend)
			if err != nil || len(kids) == 0 {
				kids, err = parseBoxes(body, descend)
				if err != nil {
					return nil, err
				}
				n.kids = kids
			} else {
				n.full, n.kids = true, kids
			}
		default:
			kids, err := parseBoxes(body, descend)
			if err != nil {
				return nil, err
			}
			n.kids = kids
		}

		out = append(out, n)
		off += int(size)
	}

	return out, nil
}

func skipVersionFlags(body []byte) []byte {
	if len(body) < 4 {
		return nil
	}
	return body[4:]
}

// shiftChunkOffsets walks an encoded moov box and adds delta to every stco/co64
// entry at or past threshold, keeping sample data reachable after moov resized.
func shiftChunkOffsets(buf []byte, delta, threshold int64) error {
	for off := 0; off+8 <= len(buf); {
		size := int64(binary.BigEndian.Uint32(buf[off : off+4]))
		typ := string(buf[off+4 : off+8])
		header := int64(8)

		switch size {
		case 0:
			size = int64(len(buf) - off)
		case 1:
			if off+16 > len(buf) {
				return errBadBox
			}
			size = int64(binary.BigEndian.Uint64(buf[off+8 : off+16]))
			header = 16
		}
		if size < header || size > int64(len(buf)-off) { // no off+size: a 64-bit size can overflow it
			return errBadBox
		}

		body := buf[off+int(header) : off+int(size)]
		switch {
		case typ == "stco" || typ == "co64":
			if err := shiftOffsetTable(typ, body, delta, threshold); err != nil {
				return err
			}
		case containerBoxes[typ]:
			if err := shiftChunkOffsets(body, delta, threshold); err != nil {
				return err
			}
		case typ == "meta":
			// Full box: its children start after version and flags.
			if err := shiftChunkOffsets(skipVersionFlags(body), delta, threshold); err != nil {
				return err
			}
		}

		off += int(size)
	}
	return nil
}

// shiftOffsetTable adjusts one stco (32-bit) or co64 (64-bit) table in place.
func shiftOffsetTable(typ string, body []byte, delta, threshold int64) error {
	const headerLen = 8 // version + flags, then the entry count.
	if len(body) < headerLen {
		return errBadBox
	}

	width := 4
	if typ == "co64" {
		width = 8
	}
	count := int(binary.BigEndian.Uint32(body[4:8]))
	if headerLen+count*width > len(body) {
		return errBadBox
	}

	for i := range count {
		at := body[headerLen+i*width:]
		if width == 4 {
			current := int64(binary.BigEndian.Uint32(at[:4]))
			if current < threshold {
				continue
			}
			shifted := current + delta
			if shifted < 0 || shifted > 0xFFFFFFFF {
				return fmt.Errorf("%w: chunk offset does not fit in stco", errBadBox)
			}
			binary.BigEndian.PutUint32(at[:4], uint32(shifted))
		} else {
			current := int64(binary.BigEndian.Uint64(at[:8]))
			if current < threshold {
				continue
			}
			shifted := current + delta
			if shifted < 0 {
				return fmt.Errorf("%w: negative chunk offset in co64", errBadBox)
			}
			binary.BigEndian.PutUint64(at[:8], uint64(shifted))
		}
	}
	return nil
}
