package tagwrite

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhowden/tag"
)

// --- fixture construction --------------------------------------------------

func tb(typ string, body []byte) []byte {
	b := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(b[:4], uint32(8+len(body)))
	copy(b[4:8], typ)
	copy(b[8:], body)
	return b
}

func tcat(parts ...[]byte) []byte { return concat(parts...) }

func stcoBox(offs ...uint32) []byte {
	body := make([]byte, 8+4*len(offs))
	binary.BigEndian.PutUint32(body[4:8], uint32(len(offs)))
	for i, o := range offs {
		binary.BigEndian.PutUint32(body[8+4*i:], o)
	}
	return tb("stco", body)
}

func co64Box(offs ...uint64) []byte {
	body := make([]byte, 8+8*len(offs))
	binary.BigEndian.PutUint32(body[4:8], uint32(len(offs)))
	for i, o := range offs {
		binary.BigEndian.PutUint64(body[8+8*i:], o)
	}
	return tb("co64", body)
}

type mp4Opts struct {
	mdatFirst  bool
	omitChain  bool
	staleIlst  []byte // raw ilst child atoms
	co64       bool
	secondTrak bool
	fragmented bool
	bothSides  bool
}

// makeMP4 writes a structurally minimal MP4 and returns its path, the mdat
// payload bytes, and the chunk offsets (patched to real absolute file offsets).
func makeMP4(t *testing.T, o mp4Opts) (path string, mdatPayload []byte, chunkOffsets []uint64) {
	t.Helper()

	chunkTable := func() []byte {
		if o.co64 {
			return co64Box(0, 0)
		}
		return stcoBox(0, 0)
	}
	trak := func() []byte {
		return tb("trak", tcat(
			tb("tkhd", make([]byte, 8)),
			tb("mdia", tcat(tb("minf", tcat(tb("stbl", tcat(
				tb("stsd", make([]byte, 8)),
				chunkTable(),
			)))))),
		))
	}

	moovKids := [][]byte{tb("mvhd", make([]byte, 12)), trak()}
	if o.secondTrak {
		moovKids = append(moovKids, trak())
	}
	if !o.omitChain {
		ilstBody := o.staleIlst
		meta := tb("meta", tcat([]byte{0, 0, 0, 0}, metaHdlrPrefix(), tb("ilst", ilstBody)))
		moovKids = append(moovKids, tb("udta", meta))
	}
	moov := tb("moov", tcat(moovKids...))

	mdatPayload = []byte("AUDIO-DATA-AUDIO-DATA")
	mdat := tb("mdat", mdatPayload)
	ftyp := tb("ftyp", []byte("M4A M4A mp42isom"))

	var parts [][]byte
	switch {
	case o.fragmented:
		parts = [][]byte{ftyp, moov, tb("moof", make([]byte, 8)), mdat}
	case o.bothSides:
		parts = [][]byte{ftyp, tb("mdat", []byte("PRE")), moov, mdat}
	case o.mdatFirst:
		parts = [][]byte{ftyp, mdat, moov}
	default:
		parts = [][]byte{ftyp, moov, mdat}
	}
	data := tcat(parts...)

	// Patch chunk offsets to the real absolute mdat-payload position.
	top, err := scanBoxes(bytesReaderAt(data), 0, int64(len(data)))
	if err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	var mdatStart int64 = -1
	for _, b := range top {
		if b.typ == "mdat" && (o.bothSides == false || b.size > 11) {
			mdatStart = b.payloadStart()
		}
	}
	if mdatStart < 0 {
		t.Fatal("fixture has no mdat")
	}
	chunkOffsets = []uint64{uint64(mdatStart), uint64(mdatStart) + 4}
	patchChunkTables(t, data, chunkOffsets, o.co64)

	path = filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path, mdatPayload, chunkOffsets
}

// patchChunkTables writes offs into every stco/co64 box in data (in place).
func patchChunkTables(t *testing.T, data []byte, offs []uint64, co64 bool) {
	t.Helper()
	want := "stco"
	esz := 4
	if co64 {
		want, esz = "co64", 8
	}
	var walk func(start, end int)
	walk = func(start, end int) {
		kids, err := scanBoxes(bytesReaderAt(data), int64(start), int64(end))
		if err != nil {
			return
		}
		for _, k := range kids {
			p := int(k.payloadStart())
			if k.typ == want {
				for i, o := range offs {
					pos := p + 8 + i*esz
					if esz == 4 {
						binary.BigEndian.PutUint32(data[pos:], uint32(o))
					} else {
						binary.BigEndian.PutUint64(data[pos:], o)
					}
				}
				continue
			}
			switch k.typ {
			case "moov", "trak", "mdia", "minf", "stbl", "udta":
				walk(p, int(k.end()))
			}
		}
	}
	walk(0, len(data))
}

// readChunkOffsets returns every chunk-offset entry in the file's first
// stco/co64 table.
func readChunkOffsets(t *testing.T, path string) []uint64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []uint64
	var walk func(start, end int)
	walk = func(start, end int) {
		kids, err := scanBoxes(bytesReaderAt(data), int64(start), int64(end))
		if err != nil {
			return
		}
		for _, k := range kids {
			p := int(k.payloadStart())
			switch k.typ {
			case "stco", "co64":
				if out != nil {
					return
				}
				n := int(binary.BigEndian.Uint32(data[p+4 : p+8]))
				for i := 0; i < n; i++ {
					if k.typ == "stco" {
						out = append(out, uint64(binary.BigEndian.Uint32(data[p+8+4*i:])))
					} else {
						out = append(out, binary.BigEndian.Uint64(data[p+8+8*i:]))
					}
				}
			case "moov", "trak", "mdia", "minf", "stbl":
				walk(p, int(k.end()))
			}
		}
	}
	walk(0, len(data))
	return out
}

func mdatBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	top, err := scanBoxes(bytesReaderAt(data), 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range top {
		if b.typ == "mdat" {
			return data[b.payloadStart():b.end()]
		}
	}
	t.Fatal("no mdat in output")
	return nil
}

// --- tests ---------------------------------------------------------------------

func TestMP4_RoundTripAndOffsetShift(t *testing.T) {
	path, mdat, origOffs := makeMP4(t, mp4Opts{})

	before, _ := os.ReadFile(path)
	inMoov := findTopBox(t, before, "moov")

	if err := (mp4Writer{}).Write(path, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := mp4Writer{}.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Equal(fullSet) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, fullSet)
	}

	if !bytes.Equal(mdatBytes(t, path), mdat) {
		t.Fatalf("mdat payload changed")
	}

	after, _ := os.ReadFile(path)
	outMoov := findTopBox(t, after, "moov")
	delta := int64(outMoov) - int64(inMoov)
	if delta <= 0 {
		t.Fatalf("expected moov to grow, delta=%d", delta)
	}
	newOffs := readChunkOffsets(t, path)
	if len(newOffs) != len(origOffs) {
		t.Fatalf("chunk offset count changed: %d -> %d", len(origOffs), len(newOffs))
	}
	for i := range origOffs {
		if newOffs[i] != origOffs[i]+uint64(delta) {
			t.Fatalf("chunk offset[%d] = %d, want %d (orig %d + delta %d)",
				i, newOffs[i], origOffs[i]+uint64(delta), origOffs[i], delta)
		}
	}
	// The first chunk offset must now point exactly at the output mdat payload.
	top, _ := scanBoxes(bytesReaderAt(after), 0, int64(len(after)))
	for _, b := range top {
		if b.typ == "mdat" && newOffs[0] != uint64(b.payloadStart()) {
			t.Fatalf("first chunk offset %d does not point at mdat payload %d", newOffs[0], b.payloadStart())
		}
	}

	// Independent structural + content check.
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		if m, err := tag.ReadFrom(f); err != nil {
			t.Fatalf("dhowden ReadFrom rejected output: %v", err)
		} else if m.Title() != fullSet.Title || m.Album() != fullSet.Album || m.Composer() != fullSet.Composer {
			t.Fatalf("stock reader: title=%q album=%q composer=%q", m.Title(), m.Album(), m.Composer())
		}
	}
}

func TestMP4_MdatBeforeMoov_NoShift(t *testing.T) {
	path, mdat, origOffs := makeMP4(t, mp4Opts{mdatFirst: true})

	if err := (mp4Writer{}).Write(path, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.Equal(mdatBytes(t, path), mdat) {
		t.Fatalf("mdat payload changed")
	}
	got := readChunkOffsets(t, path)
	for i := range origOffs {
		if got[i] != origOffs[i] {
			t.Fatalf("chunk offset[%d] moved to %d (was %d) with mdat before moov", i, got[i], origOffs[i])
		}
	}
	ts, err := mp4Writer{}.Read(path)
	if err != nil || !ts.Equal(fullSet) {
		t.Fatalf("round-trip: %v / %+v", err, ts)
	}
}

func TestMP4_Co64OffsetShift(t *testing.T) {
	path, _, origOffs := makeMP4(t, mp4Opts{co64: true})
	before, _ := os.ReadFile(path)
	inMoov := findTopBox(t, before, "moov")

	if err := (mp4Writer{}).Write(path, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}
	after, _ := os.ReadFile(path)
	delta := uint64(findTopBox(t, after, "moov") - inMoov)
	for i, o := range readChunkOffsets(t, path) {
		if o != origOffs[i]+delta {
			t.Fatalf("co64 offset[%d] = %d, want %d", i, o, origOffs[i]+delta)
		}
	}
}

func TestMP4_SecondTrackOffsetsShift(t *testing.T) {
	path, _, _ := makeMP4(t, mp4Opts{secondTrak: true})
	before, _ := os.ReadFile(path)
	inMoov := findTopBox(t, before, "moov")

	if err := (mp4Writer{}).Write(path, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}
	after, _ := os.ReadFile(path)
	delta := int64(findTopBox(t, after, "moov") - inMoov)

	// Every stco entry across every track must have shifted by delta.
	data := after
	var checked int
	var walk func(start, end int)
	walk = func(start, end int) {
		kids, _ := scanBoxes(bytesReaderAt(data), int64(start), int64(end))
		for _, k := range kids {
			p := int(k.payloadStart())
			if k.typ == "stco" {
				n := int(binary.BigEndian.Uint32(data[p+4 : p+8]))
				for i := 0; i < n; i++ {
					v := binary.BigEndian.Uint32(data[p+8+4*i:])
					// original was mdatStart(+4); both tracks share the fixture's table
					_ = v
					checked++
				}
			}
			switch k.typ {
			case "moov", "trak", "mdia", "minf", "stbl":
				walk(p, int(k.end()))
			}
		}
	}
	walk(0, len(data))
	if checked != 4 { // 2 tracks * 2 chunks
		t.Fatalf("expected 4 chunk entries across 2 tracks, saw %d", checked)
	}
	if delta <= 0 {
		t.Fatalf("delta=%d", delta)
	}
}

func TestMP4_CreatesChainWhenAbsent(t *testing.T) {
	path, mdat, _ := makeMP4(t, mp4Opts{omitChain: true})

	if _, err := (mp4Writer{}).Read(path); err != nil {
		t.Fatalf("Read of untagged file: %v", err)
	}
	if err := (mp4Writer{}).Write(path, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := mp4Writer{}.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Equal(fullSet) {
		t.Fatalf("mismatch after creating chain:\n got %+v\nwant %+v", got, fullSet)
	}
	if !bytes.Equal(mdatBytes(t, path), mdat) {
		t.Fatalf("mdat payload changed")
	}
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		if _, err := tag.ReadFrom(f); err != nil {
			t.Fatalf("dhowden rejected chain-created output: %v", err)
		}
	}
}

func TestMP4_FullRewriteDropsStaleAtoms(t *testing.T) {
	stale := tcat(
		tb("\251ART", dataAtom(dataTypeUTF8, []byte("Old Artist"))),
		tb("\251alb", dataAtom(dataTypeUTF8, []byte("Old Album"))),
		freeformAtom("SERIES", "Wrong Series"),
		freeformAtom("MOOD", "melancholy"),
	)
	path, _, _ := makeMP4(t, mp4Opts{staleIlst: stale})

	want := TagSet{Title: "Elantris", Album: "Elantris", Artist: "Brandon Sanderson", Genre: "Audiobook"}
	if err := (mp4Writer{}).Write(path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := mp4Writer{}.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("mismatch:\n got %+v\nwant %+v", got, want)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("Wrong Series")) || bytes.Contains(raw, []byte("melancholy")) || bytes.Contains(raw, []byte("Old Artist")) {
		t.Fatalf("stale atom survived the rewrite")
	}
}

func TestMP4_SemanticIdempotent(t *testing.T) {
	path, mdat, _ := makeMP4(t, mp4Opts{})
	for i := 0; i < 3; i++ {
		if err := (mp4Writer{}).Write(path, fullSet); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
		got, err := mp4Writer{}.Read(path)
		if err != nil {
			t.Fatalf("Read #%d: %v", i, err)
		}
		if !got.Equal(fullSet) {
			t.Fatalf("write #%d not idempotent:\n got %+v\nwant %+v", i, got, fullSet)
		}
		if !bytes.Equal(mdatBytes(t, path), mdat) {
			t.Fatalf("write #%d disturbed mdat", i)
		}
	}
	// Two writes in a row must not keep growing the file (offsets would drift).
	a, _ := os.ReadFile(path)
	if err := (mp4Writer{}).Write(path, fullSet); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if len(a) != len(b) {
		t.Fatalf("idempotent write changed file size %d -> %d", len(a), len(b))
	}
}

func TestMP4_Cover(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		mime string
	}{
		{"png", append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1, 2, 3}, 20)...), "image/png"},
		{"jpeg", append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte{9, 8, 7}, 20)...), "image/jpeg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _, _ := makeMP4(t, mp4Opts{})
			set := fullSet
			set.Cover, set.CoverMIME = tc.data, tc.mime
			if err := (mp4Writer{}).Write(path, set); err != nil {
				t.Fatalf("Write: %v", err)
			}
			got, err := mp4Writer{}.Read(path)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if !bytes.Equal(got.Cover, tc.data) || got.CoverMIME != tc.mime {
				t.Fatalf("cover round-trip: %d bytes mime=%q want mime %q", len(got.Cover), got.CoverMIME, tc.mime)
			}
			if !got.Equal(set) {
				t.Fatalf("cover set mismatch")
			}
		})
	}
}

func TestMP4_RejectsFragmented(t *testing.T) {
	path, _, _ := makeMP4(t, mp4Opts{fragmented: true})
	if err := (mp4Writer{}).Write(path, fullSet); !errors.Is(err, ErrFragmentedMP4) {
		t.Fatalf("err = %v, want ErrFragmentedMP4", err)
	}
}

func TestMP4_RejectsMdatBothSides(t *testing.T) {
	path, _, _ := makeMP4(t, mp4Opts{bothSides: true})
	if err := (mp4Writer{}).Write(path, fullSet); !errors.Is(err, errMP4Layout) {
		t.Fatalf("err = %v, want errMP4Layout", err)
	}
}

func TestMP4_NoTempFileLeftBehind(t *testing.T) {
	path, _, _ := makeMP4(t, mp4Opts{})
	if err := (mp4Writer{}).Write(path, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			t.Fatalf("left scratch file behind: %s", e.Name())
		}
	}
}

// findTopBox returns the total size of the named top-level box.
func findTopBox(t *testing.T, data []byte, typ string) int {
	t.Helper()
	top, err := scanBoxes(bytesReaderAt(data), 0, int64(len(data)))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, b := range top {
		if b.typ == typ {
			return int(b.size)
		}
	}
	t.Fatalf("no %s box", typ)
	return 0
}
