package main

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
)

// Tokenisation turns a log line into a *template hash* plus a list of tokens.
//
//   - A token without digits is a literal: its bytes are part of the template.
//   - A token with digits is a slot: only its *shape* (digit runs collapsed to
//     '#', random-looking segments collapsed to "<*>") is part of the template;
//     the concrete value is tracked separately per template by the model.
//
// The template hash is a position-keyed sum of token hashes, which makes the
// "same line with token i removed" hash an O(1) subtraction. The model uses
// that to learn wildcard positions from baselines that disagree with each other.

const (
	maxLine   = 64 << 10 // longer lines are truncated
	maxTokens = 512      // the rest of a longer line is folded into one final literal token
	dateHash  = 0xda7e   // every month and weekday name hashes to this
	fnvOffset = 14695981039346656037
	fnvPrime  = 1099511628211
	lenSalt   = 0x9E3779B97F4A7C15
)

type token struct {
	h    uint64
	s, e int32
	slot bool // contains digits; value tracked per template
	vol  bool // inherently volatile: duration, size, hash, timestamp, ...
	num  bool // bare number; a following unit word ("ms", "seconds") makes it volatile
	rnd  bool // machine-generated id (hex digest, uuid, timestamp): the position is a wildcard
}

type scanner struct {
	toks  []token
	clean []byte
	shape []byte
	unit  bool
}

// prefixKey identifies "any line starting with these k tokens" (sum of their mixed hashes).
func prefixKey(sum uint64, k int) uint64 { return mix(sum, k) ^ 0x3c3c3c3c3c3c3c3c }

// minPrefix is how many literal (digit-free) tokens a prefix needs before it
// may stand for a whole line. Timestamps and other slots do not count, or
// "<date> <time> INFO [main]" would make every line of such a log free text.
const minPrefix = 4

// idKey narrows a leave-one-out key to tokens of one byte length: a position
// that held a generated id accepts another id of the same length, not "NONE".
func idKey(loo uint64, length int) uint64 { return loo ^ mix(uint64(length), 77) }

var sepTab, trimTab, alnumTab, hexTab [256]bool

func init() {
	for c := 0; c <= ' '; c++ {
		sepTab[c] = true
	}
	sepTab[0x7f] = true
	for _, c := range []byte(",;()[]{}\"'<>|=`") {
		sepTab[c] = true
	}
	for _, c := range []byte(":.!?") {
		trimTab[c] = true
	}
	for c := 0; c < 256; c++ {
		alnumTab[c] = c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
		hexTab[c] = c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
	}
}

func hashBytes(b []byte) uint64 {
	h := uint64(fnvOffset)
	for _, c := range b {
		h = (h ^ uint64(c)) * fnvPrime
	}
	return h
}

func mix(h uint64, pos int) uint64 {
	x := h + uint64(pos+1)*lenSalt
	x ^= x >> 32
	x *= 0xd6e8feb86659fd93
	x ^= x >> 32
	return x
}

// looKey identifies "template lineHash with the token at pos removed".
func looKey(lineHash, tokHash uint64, pos int) uint64 {
	return mix(lineHash-mix(tokHash, pos), pos) ^ 0xa5a5a5a5a5a5a5a5
}

// cleanLine strips the line terminator, ANSI escape sequences and everything
// before the last carriage return (progress bars redraw the same line) -
// unless what the redraw would hide reads like a failure.
// The result aliases b or the scanner's scratch buffer.
func (sc *scanner) cleanLine(b []byte) []byte {
	b = bytes.TrimRight(b, "\r\n")
	if i := bytes.LastIndexByte(b, '\r'); i >= 0 {
		if lexMatch(b[:i]) {
			sc.clean = append(sc.clean[:0], b...)
			b = sc.clean
			for k, c := range b {
				if c == '\r' {
					b[k] = ' '
				}
			}
		} else {
			b = b[i+1:]
		}
	}
	if bytes.IndexByte(b, 0x1b) < 0 {
		return b
	}
	out := sc.clean[:0] // may alias b: safe, the write index never passes the read index
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c != 0x1b {
			out = append(out, c)
			continue
		}
		if i+1 >= len(b) {
			break
		}
		switch b[i+1] {
		case '[': // CSI ... final byte 0x40-0x7e
			i += 2
			for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
				i++
			}
		case ']': // OSC ... terminated by BEL or ESC \
			j := i + 2
			for j < len(b) && b[j] != 0x07 && b[j] != 0x1b {
				j++
			}
			if j == len(b) { // unterminated: drop the introducer, keep the text
				i++
				break
			}
			if i = j; b[i] == 0x1b && i+1 < len(b) {
				i++
			}
		default:
			i++
		}
	}
	sc.clean = out
	return out
}

// scan tokenises b and returns its template hash. ok is false for lines with
// no tokens (blank or pure punctuation).
func (sc *scanner) scan(b []byte) (hash uint64, ok bool) {
	toks := sc.toks[:0]
	n := len(b)
	for i := 0; i < n; {
		for i < n && sepTab[b[i]] {
			i++
		}
		s := i
		if len(toks) == maxTokens && s < n { // fold the rest: a change out there is still a change
			toks = append(toks, token{h: hashBytes(b[s:]), s: int32(s), e: int32(n)})
			break
		}
		for i < n && !sepTab[b[i]] {
			i++
		}
		e := i
		for e > s && trimTab[b[e-1]] {
			e--
		}
		if e == s {
			if bytes.IndexByte(b[s:i], '!') < 0 {
				continue
			}
			e = i // "!" alone is what is left of "!=": keep it, "==" vs "!=" matters
		}
		t := b[s:e]
		tk := token{s: int32(s), e: int32(e)}
		digit := false
		h := uint64(fnvOffset)
		for _, c := range t {
			if c-'0' < 10 {
				digit = true
			}
			h = (h ^ uint64(c)) * fnvPrime
		}
		if digit {
			tk.slot = true
			tk.vol, tk.num = sc.shapeOf(t)
			tk.rnd = tk.vol && !tk.num && bytes.Contains(sc.shape, []byte("<*>"))
			h = hashBytes(sc.shape) ^ 0x5555
		} else if isDateWord(t) {
			h = dateHash
		}
		tk.h = h
		toks = append(toks, tk)
	}
	sc.toks = toks
	if len(toks) == 0 {
		return 0, false
	}
	var sum uint64
	for i := range toks {
		t := &toks[i]
		if t.num && !t.vol {
			// "12 ms" is a measurement; "Nov 10" is a date.
			if i+1 < len(toks) && !toks[i+1].slot && (toks[i+1].h == dateHash || isUnit(b[toks[i+1].s:toks[i+1].e])) {
				t.vol = true
			} else if i > 0 && toks[i-1].h == dateHash {
				t.vol = true
			}
		}
		sum += mix(t.h, i)
	}
	return sum + uint64(len(toks))*lenSalt, true
}

// shapeOf writes the normalised shape of digit-bearing token t to sc.shape.
func (sc *scanner) shapeOf(t []byte) (vol, num bool) {
	if sc.buildShape(t, false) {
		sc.buildShape(t, true)
		return true, false
	}
	sh := sc.shape
	if sc.unit || sh[len(sh)-1] == '%' {
		return true, false
	}
	switch string(sh) {
	case "#", "#:#":
		return false, true
	case "#.#": // 3+ fractional digits is a measurement, not a version
		return len(t)-bytes.IndexByte(t, '.')-1 >= 3, true
	case "#:#:#", "#:#:#.#", "#:#.#", "#-#-#", "#/#/#":
		return true, false
	}
	return false, false
}

// buildShape walks the alphanumeric segments of t. In the first pass
// (random=false) it reports whether any segment looks machine-generated
// (long hex, or interleaved letters and digits). If so the caller re-runs it
// with random=true, which also collapses the short hex/digit segments around
// it so that every UUID, digest or timestamp gets the same shape.
func (sc *scanner) buildShape(t []byte, random bool) (found bool) {
	sh := sc.shape[:0]
	sc.unit = false
	for i := 0; i < len(t); {
		c := t[i]
		if !alnumTab[c] {
			sh = append(sh, c)
			i++
			continue
		}
		j, nd, trans, hex := i, 0, 0, true
		for ; j < len(t) && alnumTab[t[j]]; j++ {
			d := t[j]-'0' < 10
			if d {
				nd++
			} else if !hexTab[t[j]] {
				hex = false
			}
			if j > i && d != (t[j-1]-'0' < 10) {
				trans++
			}
		}
		seg := t[i:j]
		nl := len(seg) - nd
		if len(seg) > 2 && seg[0] == '0' && seg[1]|0x20 == 'x' && isHex(seg[2:]) {
			// 0xC0000005 is a status code or an address, not an id: track it.
			sh = append(sh, "0x#"...)
			i = j
			continue
		}
		switch {
		case random && (nd > 0 || hex && len(seg) >= 4):
			sh = append(sh, "<*>"...)
		case nl == 0:
			sh = append(sh, '#')
		case nd == 0:
			sh = append(sh, seg...)
		case hex && len(seg) >= 7, trans > 1 && len(seg) >= 5:
			found = true
			sh = append(sh, "<*>"...)
		case trans > 1: // short mixed words: k8s, i18n, e2e
			sh = append(sh, seg...)
		case seg[0]-'0' < 10: // digits then letters: 120ms, 3rd
			sh = append(sh, '#')
			sh = append(sh, seg[nd:]...)
			if isUnit(seg[nd:]) {
				sc.unit = true
			}
		default: // letters then digits: TS2304, sha256, v1
			sh = append(sh, seg[:nl]...)
			sh = append(sh, '#')
		}
		i = j
	}
	sc.shape = sh
	return found
}

func isHex(b []byte) bool {
	for _, c := range b {
		if !hexTab[c] {
			return false
		}
	}
	return true
}

func isUnit(b []byte) bool {
	if len(b) > 12 {
		return false
	}
	var buf [12]byte
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		buf[i] = c
	}
	switch string(buf[:len(b)]) {
	case "ns", "us", "µs", "ms", "s", "m", "h", "sec", "secs", "second", "seconds",
		"min", "mins", "minute", "minutes", "hr", "hrs", "hour", "hours", "msec",
		"millis", "milliseconds", "b", "kb", "mb", "gb", "tb", "kib", "mib", "gib",
		"k", "g", "bytes", "kbps", "mbps":
		return true
	}
	return false
}

func isDateWord(t []byte) bool {
	switch string(t) {
	case "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
		"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun":
		return true
	}
	return false
}

func applyMasks(b []byte, masks []*regexp.Regexp) []byte {
	for _, re := range masks {
		b = re.ReplaceAll(b, []byte(" <masked> "))
	}
	return b
}

// eachLine calls fn for every line of r. The slice is only valid during the
// call. Lines longer than maxLine are truncated, never buffered whole.
func eachLine(r io.Reader, fn func(line []byte)) error {
	br := bufio.NewReaderSize(r, maxLine)
	for {
		line, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			fn(line)
			for err == bufio.ErrBufferFull {
				_, err = br.ReadSlice('\n')
			}
		} else if len(line) > 0 {
			fn(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
