package tagwrite

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// mp4Writer rewrites the iTunes-style metadata (moov/udta/meta/ilst) of an
// MP4-container audiobook: .m4b and .m4a.
//
// Full rewrite: the ilst box is replaced wholesale (the chain is created if the
// file has none), so no stale atom survives. STREAMINFO-equivalent structure —
// every other box, including chapter tracks — is copied verbatim and the audio
// in mdat is preserved byte for byte. When the metadata size changes and mdat
// sits after moov, every chunk-offset table (stco/co64) in every track is
// shifted by the same delta so the sample data still resolves.
//
//	TagSet field   atom
//	Title          ©nam
//	Album          ©alb
//	Artist         ©ART
//	AlbumArtist    aART
//	Composer       ©wrt
//	Genre          ©gen
//	Year           ©day
//	Comment        ©cmt
//	Track          trkn  (number + total)
//	Series         ----:com.apple.iTunes:SERIES
//	SeriesIndex    ----:com.apple.iTunes:SERIES-PART
//	Subtitle       ----:com.apple.iTunes:SUBTITLE
//	Composer       ----:com.apple.iTunes:NARRATOR  (mirror of ©wrt)
//	ASIN           ----:com.apple.iTunes:ASIN
//	ISBN           ----:com.apple.iTunes:ISBN
//	Cover          covr  (JPEG or PNG)
type mp4Writer struct{}

// Ext reports ".m4b" as the representative extension; WriterFor also routes
// ".m4a" here. The two share a container and a writer.
func (mp4Writer) Ext() string { return ".m4b" }

// ErrFragmentedMP4 is returned for a fragmented MP4 (one with a moof box).
// Audiobooks are never fragmented, and rewriting one safely needs machinery
// this writer deliberately does not carry.
var ErrFragmentedMP4 = errors.New("tagwrite: fragmented MP4 is not supported")

// errMP4Layout is returned when mdat sits both before and after moov, so a
// single chunk-offset delta cannot be correct for every track.
var errMP4Layout = errors.New("tagwrite: unsupported MP4 box layout (mdat both sides of moov)")

func (mp4Writer) Write(path string, ts TagSet) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()

	fi, err := src.Stat()
	if err != nil {
		return err
	}
	top, err := scanBoxes(src, 0, fi.Size())
	if err != nil {
		return err
	}

	var moov *box
	moovIdx := -1
	for i := range top {
		switch top[i].typ {
		case "moov":
			if moov != nil {
				return fmt.Errorf("tagwrite: MP4 has more than one moov box")
			}
			moov = &top[i]
			moovIdx = i
		case "moof", "mfra":
			return ErrFragmentedMP4
		}
	}
	if moov == nil {
		return fmt.Errorf("tagwrite: MP4 has no moov box")
	}
	if moov.largeSize {
		return fmt.Errorf("tagwrite: moov box uses 64-bit size")
	}

	mdatBefore, mdatAfter := false, false
	for i := range top {
		if top[i].typ != "mdat" {
			continue
		}
		if i < moovIdx {
			mdatBefore = true
		} else {
			mdatAfter = true
		}
	}
	if mdatBefore && mdatAfter {
		return errMP4Layout
	}

	mv := make([]byte, moov.size)
	if _, err := src.ReadAt(mv, moov.off); err != nil {
		return fmt.Errorf("read moov: %w", err)
	}

	newMoov, err := rebuildMoov(mv, ts, mdatAfter)
	if err != nil {
		return err
	}

	return replaceFile(path, func(w io.Writer) error {
		for i := range top {
			if i == moovIdx {
				if _, err := w.Write(newMoov); err != nil {
					return err
				}
				continue
			}
			if _, err := src.Seek(top[i].off, io.SeekStart); err != nil {
				return err
			}
			if _, err := io.CopyN(w, src, top[i].size); err != nil {
				return fmt.Errorf("copy %s box: %w", top[i].typ, err)
			}
		}
		return src.Close()
	})
}

func (mp4Writer) Read(path string) (TagSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return TagSet{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return TagSet{}, err
	}
	top, err := scanBoxes(f, 0, fi.Size())
	if err != nil {
		return TagSet{}, err
	}
	var moov *box
	for i := range top {
		if top[i].typ == "moov" {
			moov = &top[i]
		}
	}
	if moov == nil {
		return TagSet{}, fmt.Errorf("tagwrite: MP4 has no moov box")
	}
	mv := make([]byte, moov.size)
	if _, err := f.ReadAt(mv, moov.off); err != nil {
		return TagSet{}, err
	}

	ilst, ok := locateIlst(mv)
	if !ok {
		return TagSet{}, nil
	}
	return parseIlst(mv[ilst.payloadStart():ilst.end()]), nil
}

// --- box model -------------------------------------------------------------

// box is one parsed MP4 box. off/size are absolute within whatever buffer or
// file it was scanned from; hdrLen is 8 (32-bit size) or 16 (64-bit size).
type box struct {
	typ       string
	off       int64
	size      int64 // total, including header
	hdrLen    int64
	largeSize bool
}

func (b box) payloadStart() int64 { return b.off + b.hdrLen }
func (b box) end() int64          { return b.off + b.size }

// scanBoxes parses the sibling boxes in [start,end) of r.
func scanBoxes(r io.ReaderAt, start, end int64) ([]box, error) {
	var out []box
	pos := start
	for pos+8 <= end {
		var hdr [16]byte
		if _, err := r.ReadAt(hdr[:8], pos); err != nil {
			return nil, err
		}
		size := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		hdrLen := int64(8)
		large := false
		switch {
		case size == 1:
			if _, err := r.ReadAt(hdr[8:16], pos+8); err != nil {
				return nil, err
			}
			size = int64(binary.BigEndian.Uint64(hdr[8:16]))
			hdrLen = 16
			large = true
		case size == 0:
			size = end - pos // extends to the end of the enclosing range
		}
		if size < hdrLen || pos+size > end {
			return nil, fmt.Errorf("tagwrite: malformed box %q at %d (size %d)", typ, pos, size)
		}
		out = append(out, box{typ: typ, off: pos, size: size, hdrLen: hdrLen, largeSize: large})
		pos += size
	}
	return out, nil
}

// childBoxes parses the boxes directly inside buf[start:end].
func childBoxes(buf []byte, start, end int) ([]box, error) {
	if start < 0 || end > len(buf) || start > end {
		return nil, fmt.Errorf("tagwrite: child range [%d,%d) outside buffer", start, end)
	}
	return scanBoxes(bytesReaderAt(buf), int64(start), int64(end))
}

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(b)) {
		return 0, fmt.Errorf("offset out of range")
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// --- locating the ilst chain --------------------------------------------------

// locateIlstChain returns the ancestor path to the ilst box inside a moov
// buffer — [udta, meta, ilst] or [meta, ilst] — with every offset relative to
// mv. The last element is the ilst; the rest are the containers whose size
// fields move when it is replaced.
func locateIlstChain(mv []byte) ([]box, bool) {
	moovKids, err := childBoxes(mv, int(boxHeaderLen(mv)), len(mv))
	if err != nil {
		return nil, false
	}
	for _, want := range []string{"udta", "meta"} {
		for _, k := range moovKids {
			if k.typ != want {
				continue
			}
			if chain, ok := ilstChainUnder(mv, k, want == "meta"); ok {
				return chain, true
			}
		}
	}
	return nil, false
}

// ilstChainUnder resolves the meta/ilst (or ilst) path beneath a udta or meta
// box and returns the full chain starting at top.
func ilstChainUnder(mv []byte, top box, topIsMeta bool) ([]box, bool) {
	chain := []box{top}
	metaBox := top
	if !topIsMeta {
		kids, err := childBoxes(mv, int(top.payloadStart()), int(top.end()))
		if err != nil {
			return nil, false
		}
		found := false
		for _, k := range kids {
			if k.typ == "meta" {
				metaBox, found = k, true
				break
			}
		}
		if !found {
			return nil, false
		}
		chain = append(chain, metaBox)
	}
	inner, err := metaChildrenStart(mv, metaBox)
	if err != nil {
		return nil, false
	}
	kids, err := childBoxes(mv, inner, int(metaBox.end()))
	if err != nil {
		return nil, false
	}
	for _, k := range kids {
		if k.typ == "ilst" {
			return append(chain, k), true
		}
	}
	return nil, false
}

// locateIlst returns just the ilst box.
func locateIlst(mv []byte) (box, bool) {
	chain, ok := locateIlstChain(mv)
	if !ok {
		return box{}, false
	}
	return chain[len(chain)-1], true
}

// metaChildrenStart returns the offset (within mv) where a meta box's child
// boxes begin. iTunes/ISO meta is a FullBox (4 version/flags bytes precede the
// children); QuickTime meta is a plain box. The layout is detected by checking
// which interpretation yields a valid first child.
func metaChildrenStart(mv []byte, meta box) (int, error) {
	plain := int(meta.payloadStart())
	full := plain + 4
	if looksLikeBox(mv, full, int(meta.end())) {
		return full, nil
	}
	if looksLikeBox(mv, plain, int(meta.end())) {
		return plain, nil
	}
	return 0, fmt.Errorf("tagwrite: unrecognised meta box layout")
}

func looksLikeBox(buf []byte, start, end int) bool {
	if start+8 > end || start+8 > len(buf) {
		return false
	}
	size := int(binary.BigEndian.Uint32(buf[start : start+4]))
	if size < 8 || start+size > end {
		return false
	}
	for _, c := range buf[start+4 : start+8] {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func boxHeaderLen(mv []byte) int64 {
	if len(mv) >= 8 && binary.BigEndian.Uint32(mv[0:4]) == 1 {
		return 16
	}
	return 8
}

// --- rebuilding moov -------------------------------------------------------

func rebuildMoov(mv []byte, ts TagSet, adjustOffsets bool) ([]byte, error) {
	newMoov, err := spliceIlst(mv, buildIlst(ts))
	if err != nil {
		return nil, err
	}
	// The number of bytes moov grew (or shrank) is exactly the amount every
	// box after moov — mdat included — shifts by. When mdat sits after moov,
	// fix every chunk-offset table to match. The tables are re-located in the
	// spliced buffer, so their post-splice positions do not matter.
	if adjustOffsets {
		delta := int64(len(newMoov) - len(mv))
		if err := shiftChunkOffsets(newMoov, delta); err != nil {
			return nil, err
		}
	}
	return newMoov, nil
}

// spliceIlst returns a new moov with its ilst replaced by newIlst, creating the
// udta/meta/ilst chain when absent. Because every ancestor of ilst starts
// before it, their offsets are unchanged by the splice and only their recorded
// size grows by delta.
func spliceIlst(mv []byte, newIlst []byte) ([]byte, error) {
	chain, ok := locateIlstChain(mv)
	if !ok {
		return createChain(mv, newIlst)
	}
	ilst := chain[len(chain)-1]
	delta := int64(len(newIlst)) - ilst.size

	out := make([]byte, 0, int64(len(mv))+delta)
	out = append(out, mv[:ilst.off]...)
	out = append(out, newIlst...)
	out = append(out, mv[ilst.end():]...)

	if err := addToBoxSize(out, 0, delta); err != nil { // moov, the buffer root
		return nil, err
	}
	for _, anc := range chain[:len(chain)-1] {
		if err := addToBoxSize(out, int(anc.off), delta); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// addToBoxSize adds delta to the 32-bit size field of the box header at off.
func addToBoxSize(buf []byte, off int, delta int64) error {
	if off < 0 || off+8 > len(buf) {
		return fmt.Errorf("tagwrite: box header out of range at %d", off)
	}
	cur := binary.BigEndian.Uint32(buf[off : off+4])
	typ := string(buf[off+4 : off+8])
	if cur == 1 {
		return fmt.Errorf("tagwrite: cannot resize 64-bit box %q", typ)
	}
	n := int64(cur) + delta
	if n < 8 || n > 0xFFFFFFFF {
		return fmt.Errorf("tagwrite: box %q size out of range after edit", typ)
	}
	binary.BigEndian.PutUint32(buf[off:off+4], uint32(n))
	return nil
}

// createChain builds a new moov that contains udta/meta/hdlr/ilst, reusing
// whatever part of the chain already exists.
func createChain(mv []byte, newIlst []byte) ([]byte, error) {
	moovInner := int(boxHeaderLen(mv))
	moovKids, err := childBoxes(mv, moovInner, len(mv))
	if err != nil {
		return nil, err
	}

	var udta *box
	for i := range moovKids {
		if moovKids[i].typ == "udta" {
			udta = &moovKids[i]
			break
		}
	}

	// metaBox wraps an ilst in a fresh meta FullBox (4 zero version/flags bytes)
	// preceded by the iTunes hdlr box.
	metaBox := func(ilst []byte) []byte {
		return mp4box4("meta", concat([]byte{0, 0, 0, 0}, metaHdlrPrefix(), ilst))
	}

	var newUdtaPayload []byte
	if udta != nil {
		udtaKids, err := childBoxes(mv, int(udta.payloadStart()), int(udta.end()))
		if err != nil {
			return nil, err
		}
		var meta *box
		for i := range udtaKids {
			if udtaKids[i].typ == "meta" {
				meta = &udtaKids[i]
				break
			}
		}
		if meta != nil {
			// Insert ilst into the existing meta.
			ms, err := metaChildrenStart(mv, *meta)
			if err != nil {
				return nil, err
			}
			newMeta := concat(
				mv[meta.off:int64(ms)],
				newIlst,
				mv[ms:meta.end()],
			)
			binary.BigEndian.PutUint32(newMeta[0:4], uint32(len(newMeta)))
			newUdtaPayload = concat(
				mv[udta.payloadStart():meta.off],
				newMeta,
				mv[meta.end():udta.end()],
			)
		} else {
			newUdtaPayload = concat(mv[udta.payloadStart():udta.end()], metaBox(newIlst))
		}
		newUdta := concat(mv[udta.off:udta.payloadStart()], newUdtaPayload)
		binary.BigEndian.PutUint32(newUdta[0:4], uint32(len(newUdta)))
		out := concat(mv[:udta.off], newUdta, mv[udta.end():])
		fixMoovSize(out)
		return out, nil
	}

	newUdta := mp4box4("udta", metaBox(newIlst))
	out := concat(mv, newUdta)
	fixMoovSize(out)
	return out, nil
}

func fixMoovSize(buf []byte) {
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(buf)))
}

// metaHdlrPrefix is the hdlr box iTunes places first inside meta.
func metaHdlrPrefix() []byte {
	body := concat(
		[]byte{0, 0, 0, 0},             // version/flags
		[]byte{0, 0, 0, 0},             // predefined
		[]byte("mdir"),                 // handler type
		[]byte("appl"),                 // reserved (manufacturer)
		[]byte{0, 0, 0, 0, 0, 0, 0, 0}, // reserved
		[]byte{0},                      // name (empty, null-terminated)
	)
	return mp4box4("hdlr", body)
}

// --- chunk-offset shifting -----------------------------------------------------

// shiftChunkOffsets adds delta to every entry of every stco and co64 box found
// anywhere inside the moov buffer, editing in place.
func shiftChunkOffsets(mv []byte, delta int64) error {
	var walk func(start, end int) error
	walk = func(start, end int) error {
		kids, err := childBoxes(mv, start, end)
		if err != nil {
			return err
		}
		for _, k := range kids {
			switch k.typ {
			case "stco":
				if err := shiftEntries(mv, k, 4, delta); err != nil {
					return err
				}
			case "co64":
				if err := shiftEntries(mv, k, 8, delta); err != nil {
					return err
				}
			case "trak", "mdia", "minf", "stbl", "edts", "udta":
				if err := walk(int(k.payloadStart()), int(k.end())); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(int(boxHeaderLen(mv)), len(mv))
}

func shiftEntries(mv []byte, b box, entrySize int, delta int64) error {
	p := int(b.payloadStart())
	if p+8 > int(b.end()) {
		return fmt.Errorf("tagwrite: short %s box", b.typ)
	}
	count := int(binary.BigEndian.Uint32(mv[p+4 : p+8]))
	pos := p + 8
	if pos+count*entrySize > int(b.end()) {
		return fmt.Errorf("tagwrite: %s box entry count %d overflows box", b.typ, count)
	}
	for i := 0; i < count; i++ {
		var cur int64
		if entrySize == 4 {
			cur = int64(binary.BigEndian.Uint32(mv[pos : pos+4]))
		} else {
			cur = int64(binary.BigEndian.Uint64(mv[pos : pos+8]))
		}
		next := cur + delta
		if next < 0 {
			return fmt.Errorf("tagwrite: chunk offset underflow shifting %s", b.typ)
		}
		if entrySize == 4 {
			if next > 0xFFFFFFFF {
				return fmt.Errorf("tagwrite: chunk offset overflows 32-bit stco; file needs co64")
			}
			binary.BigEndian.PutUint32(mv[pos:pos+4], uint32(next))
		} else {
			binary.BigEndian.PutUint64(mv[pos:pos+8], uint64(next))
		}
		pos += entrySize
	}
	return nil
}

// --- ilst construction -------------------------------------------------------

const (
	dataTypeBinary = 0
	dataTypeUTF8   = 1
	dataTypeJPEG   = 13
	dataTypePNG    = 14
)

// buildIlst returns a complete ilst box for ts.
func buildIlst(ts TagSet) []byte {
	var items [][]byte
	text := func(atom, v string) {
		if v != "" {
			items = append(items, mp4box4(atom, dataAtom(dataTypeUTF8, []byte(v))))
		}
	}
	text("\251nam", ts.Title)
	text("\251alb", ts.Album)
	text("\251ART", ts.Artist)
	text("aART", ts.AlbumArtist)
	text("\251wrt", ts.Composer)
	text("\251gen", ts.Genre)
	text("\251day", yearString(ts.Year))
	text("\251cmt", ts.Comment)

	if ts.Track > 0 {
		payload := make([]byte, 8)
		binary.BigEndian.PutUint16(payload[2:4], uint16(ts.Track))
		if ts.TrackTotal > 0 {
			binary.BigEndian.PutUint16(payload[4:6], uint16(ts.TrackTotal))
		}
		items = append(items, mp4box4("trkn", dataAtom(dataTypeBinary, payload)))
	}

	free := func(name, v string) {
		if v != "" {
			items = append(items, freeformAtom(name, v))
		}
	}
	free("SERIES", ts.Series)
	free("SERIES-PART", ts.SeriesIndex)
	free("SUBTITLE", ts.Subtitle)
	free("NARRATOR", ts.Composer)
	free("ASIN", ts.ASIN)
	free("ISBN", ts.ISBN)

	if len(ts.Cover) > 0 {
		dt := dataTypeJPEG
		if isPNG(ts.Cover) || strings.Contains(strings.ToLower(ts.CoverMIME), "png") {
			dt = dataTypePNG
		}
		items = append(items, mp4box4("covr", dataAtom(uint32(dt), ts.Cover)))
	}

	return mp4box4("ilst", concat(items...))
}

// dataAtom builds an iTunes "data" atom: 4-byte type indicator, 4-byte locale,
// then the value.
func dataAtom(typ uint32, value []byte) []byte {
	body := make([]byte, 8+len(value))
	binary.BigEndian.PutUint32(body[0:4], typ)
	// bytes 4:8 locale = 0
	copy(body[8:], value)
	return mp4box4("data", body)
}

// freeformAtom builds a "----" box: mean=com.apple.iTunes, name, UTF-8 data.
func freeformAtom(name, value string) []byte {
	mean := mp4box4("mean", concat([]byte{0, 0, 0, 0}, []byte("com.apple.iTunes")))
	nm := mp4box4("name", concat([]byte{0, 0, 0, 0}, []byte(name)))
	return mp4box4("----", concat(mean, nm, dataAtom(dataTypeUTF8, []byte(value))))
}

// mp4box4 frames body as a box with a 4-character type and a 32-bit size.
func mp4box4(typ string, body []byte) []byte {
	if len(typ) != 4 {
		panic("mp4box4: type must be 4 bytes")
	}
	out := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(body)))
	copy(out[4:8], typ)
	copy(out[8:], body)
	return out
}

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func isPNG(b []byte) bool {
	return len(b) >= 8 && string(b[0:8]) == "\x89PNG\r\n\x1a\n"
}

// --- ilst parsing ----------------------------------------------------------

// parseIlst reads an ilst payload (its child boxes) into a TagSet.
func parseIlst(payload []byte) TagSet {
	var ts TagSet
	kids, err := childBoxes(payload, 0, len(payload))
	if err != nil {
		return ts
	}
	for _, k := range kids {
		body := payload[k.payloadStart():k.end()]
		if k.typ == "----" {
			name, val := parseFreeform(body)
			switch strings.ToUpper(name) {
			case "SERIES":
				ts.Series = val
			case "SERIES-PART":
				ts.SeriesIndex = val
			case "SUBTITLE":
				ts.Subtitle = val
			case "ASIN":
				ts.ASIN = val
			case "ISBN":
				ts.ISBN = val
			}
			continue
		}
		dtype, data, ok := firstDataAtom(body)
		if !ok {
			continue
		}
		switch k.typ {
		case "\251nam":
			ts.Title = strings.TrimSpace(string(data))
		case "\251alb":
			ts.Album = strings.TrimSpace(string(data))
		case "\251ART":
			ts.Artist = strings.TrimSpace(string(data))
		case "aART":
			ts.AlbumArtist = strings.TrimSpace(string(data))
		case "\251wrt":
			ts.Composer = strings.TrimSpace(string(data))
		case "\251gen":
			ts.Genre = strings.TrimSpace(string(data))
		case "\251day":
			ts.Year = parseLeadingYear(string(data))
		case "\251cmt":
			ts.Comment = strings.TrimSpace(string(data))
		case "trkn":
			if len(data) >= 6 {
				ts.Track = int(binary.BigEndian.Uint16(data[2:4]))
				ts.TrackTotal = int(binary.BigEndian.Uint16(data[4:6]))
			}
		case "covr":
			ts.Cover = append([]byte(nil), data...)
			switch dtype {
			case dataTypePNG:
				ts.CoverMIME = "image/png"
			case dataTypeJPEG:
				ts.CoverMIME = "image/jpeg"
			default:
				if isPNG(data) {
					ts.CoverMIME = "image/png"
				} else {
					ts.CoverMIME = "image/jpeg"
				}
			}
		}
	}
	return ts
}

// firstDataAtom returns the type indicator and value of the first "data" child.
func firstDataAtom(container []byte) (dtype uint32, value []byte, ok bool) {
	kids, err := childBoxes(container, 0, len(container))
	if err != nil {
		return 0, nil, false
	}
	for _, k := range kids {
		if k.typ != "data" {
			continue
		}
		b := container[k.payloadStart():k.end()]
		if len(b) < 8 {
			return 0, nil, false
		}
		return binary.BigEndian.Uint32(b[0:4]), b[8:], true
	}
	return 0, nil, false
}

func parseFreeform(container []byte) (name, value string) {
	kids, err := childBoxes(container, 0, len(container))
	if err != nil {
		return "", ""
	}
	for _, k := range kids {
		b := container[k.payloadStart():k.end()]
		switch k.typ {
		case "name":
			if len(b) >= 4 {
				name = string(b[4:])
			}
		case "data":
			if len(b) >= 8 {
				value = strings.TrimSpace(string(b[8:]))
			}
		}
	}
	return name, value
}
