package main

import (
	"io"
	"math"
	"regexp"
	"slices"
	"strings"
)

const (
	maxEntries = 20000 // anomalous lines kept in a report; the rest are only counted
	maxPerKey  = 3     // separate occurrences of one anomaly shown before it is only counted
	maxMissing = 200
	maxText    = 2000
)

// Change is a tracked value that the baselines never used.
type Change struct {
	Value    string   `json:"value"`
	Baseline []string `json:"baseline"`
}

// Entry is one line of a block. Kind is empty for context lines.
type Entry struct {
	Line        int      `json:"line"`
	Text        string   `json:"text"`
	Kind        string   `json:"kind,omitempty"` // new | changed | spike
	Score       float64  `json:"score,omitempty"`
	Repeat      int      `json:"repeat,omitempty"` // identical lines directly below, collapsed
	Total       int      `json:"total,omitempty"`  // occurrences in the whole log
	Changes     []Change `json:"changes,omitempty"`
	BaselineMax int      `json:"baseline_max,omitempty"`
	Early       bool     `json:"early,omitempty"` // value changed before the first failure

	failure bool // reads like a failure (lexicon hit)
}

// Block is a run of anomalous lines with surrounding context.
type Block struct {
	Rank      int      `json:"rank"` // 1 = most suspicious
	Line      int      `json:"line"` // first anomalous line
	End       int      `json:"end"`
	Score     float64  `json:"score"`
	Anomalies int      `json:"anomalies"`
	Entries   []*Entry `json:"entries"`

	early bool
}

type Result struct {
	Version       string   `json:"version"`
	Failing       string   `json:"failing"`
	Baselines     []string `json:"baselines"`
	Runs          int      `json:"runs"`
	BaselineLines uint64   `json:"baseline_lines"`
	Templates     int      `json:"templates"`
	Wildcards     int      `json:"wildcards"`
	Lines         uint64   `json:"lines"`
	NewLines      uint64   `json:"new_lines"`
	ChangedLines  uint64   `json:"changed_lines"`
	Spikes        uint64   `json:"spikes"`
	Blocks        []*Block `json:"blocks"`
	Missing       []string `json:"missing"`
	MissingTotal  int      `json:"missing_total"`
	Truncated     bool     `json:"truncated,omitempty"`
}

var lexicon = regexp.MustCompile(`(?i)error|fail|fatal|panic|exception|traceback|denied|refused|timed out|timeout|cannot|can't|unable|undefined|not found|no such|killed|segmentation|segfault|abort|invalid|unexpected|exit code|exit status|assert|mismatch|out of memory|unresolved|rejected|crash`)

type ringLine struct {
	n int
	b []byte
}

type supStat struct {
	first          *Entry
	entries, total int
}

type analyzer struct {
	m   *Model
	res *Result
	sc  scanner
	ctx int

	ring []ringLine
	pos  int

	cur      *Block
	prev     *Block // most recently closed block
	trailing int    // context lines still owed to cur
	lastEnd  int    // last line already shown in some block

	seen   map[uint64]uint32
	sup    map[uint64]*supStat
	spikes map[uint64]*Entry

	last     *Entry
	lastKey  uint64
	lastLine int
	entries  int
}

// analyze streams the failing log once. Memory is O(model + reported lines),
// independent of the size of the log.
func analyze(m *Model, r io.Reader, masks []*regexp.Regexp, ctx int) (*Result, error) {
	a := &analyzer{
		m: m, ctx: ctx, ring: make([]ringLine, ctx),
		seen: map[uint64]uint32{}, sup: map[uint64]*supStat{}, spikes: map[uint64]*Entry{},
		res: &Result{
			Version: version, Baselines: m.Sources, Runs: m.Runs, BaselineLines: m.Lines,
			Templates: len(m.Templates), Wildcards: m.Learned, Blocks: []*Block{}, Missing: []string{},
		},
	}
	n := 0
	err := eachLine(r, func(line []byte) {
		n++
		b := applyMasks(a.sc.cleanLine(line), masks)
		if h, ok := a.sc.scan(b); ok && a.classify(h, b, n) {
			return
		}
		a.normal(n, b)
	})
	a.res.Lines = uint64(n)
	a.finish()
	return a.res, err
}

// classify reports whether line n was consumed as an anomaly.
func (a *analyzer) classify(h uint64, b []byte, n int) bool {
	m, toks := a.m, a.sc.toks
	if t := m.Templates[h]; t != nil {
		c := a.seen[h] + 1
		a.seen[h] = c
		var ch []Change
		key := h ^ 1
		si := 0
		for _, tk := range toks {
			if !tk.slot {
				continue
			}
			if si >= len(t.Slots) {
				break
			}
			s := &t.Slots[si]
			si++
			if s.Vol {
				continue
			}
			if v := b[tk.s:tk.e]; !hasVal(s.Vals, v) {
				ch = append(ch, Change{Value: clip(v), Baseline: s.Vals})
				key = key*fnvPrime ^ hashBytes(v)
			}
		}
		if ch != nil {
			a.res.ChangedLines++
			return a.emit(key, n, b, "changed", 1.2, ch)
		}
		if c == max(3*t.Max+1, t.Max+5) {
			a.res.Spikes++
			e := &Entry{Line: n, Text: clip(b), Kind: "spike", Score: 0.8, BaselineMax: int(t.Max), failure: lexicon.Match(b)}
			if a.entries < maxEntries {
				a.spikes[h] = e
				a.add(e)
				a.last = nil
				return true
			}
		}
		return false
	}
	if len(toks) >= 3 && len(m.Wild) > 0 {
		// A wildcard absorbs arbitrary tokens, but never one that reads like
		// a failure: "PASS x" vs "FAIL x" must not be noise.
		var sum uint64
		for i, tk := range toks {
			if m.Wild[looKey(h, tk.h, i)] && !lexicon.Match(b[tk.s:tk.e]) {
				return false
			}
			if i >= minPrefix && m.Wild[prefixKey(sum, i)] && !lexicon.Match(b[tk.s:]) {
				return false
			}
			sum += mix(tk.h, i)
		}
	}
	a.res.NewLines++
	unseen := 0
	for _, tk := range toks {
		if !m.Vocab[tk.h] {
			unseen++
		}
	}
	return a.emit(h, n, b, "new", 1+2*float64(unseen)/float64(len(toks)), nil)
}

func (a *analyzer) emit(key uint64, n int, b []byte, kind string, score float64, ch []Change) bool {
	if a.last != nil && a.lastKey == key && a.lastLine == n-1 {
		a.last.Repeat++
		a.lastLine = n
		a.sup[key].total++
		a.touch(n)
		return true
	}
	s := a.sup[key]
	if s == nil {
		if a.entries >= maxEntries {
			a.res.Truncated = true
			return false
		}
		s = &supStat{}
		a.sup[key] = s
	}
	s.total++
	if s.entries >= maxPerKey {
		return false
	}
	s.entries++
	e := &Entry{Line: n, Text: clip(b), Kind: kind, Score: score, Changes: ch, failure: lexicon.Match(b)}
	if s.first == nil {
		s.first = e
	}
	a.add(e)
	a.last, a.lastKey, a.lastLine = e, key, n
	return true
}

func (a *analyzer) touch(n int) {
	a.cur.End = n
	a.trailing = a.ctx
	a.lastEnd = n
}

func (a *analyzer) add(e *Entry) {
	a.entries++
	if a.cur == nil {
		if a.prev != nil && e.Line-1-a.lastEnd <= len(a.ring) {
			a.cur = a.prev // every line in between is context we still hold: one block
		} else {
			a.cur = &Block{Line: e.Line}
			a.res.Blocks = append(a.res.Blocks, a.cur)
		}
		for k := range a.ring {
			if it := &a.ring[(a.pos+k)%len(a.ring)]; it.n > a.lastEnd {
				a.cur.Entries = append(a.cur.Entries, &Entry{Line: it.n, Text: clip(it.b)})
			}
		}
	}
	a.cur.Entries = append(a.cur.Entries, e)
	a.cur.Anomalies++
	a.touch(e.Line)
}

func (a *analyzer) normal(n int, b []byte) {
	if a.cur != nil {
		if a.trailing > 0 {
			a.cur.Entries = append(a.cur.Entries, &Entry{Line: n, Text: clip(b)})
			a.trailing--
			a.lastEnd = n
			return
		}
		a.prev, a.cur = a.cur, nil
	}
	if len(a.ring) > 0 {
		it := &a.ring[a.pos]
		it.n, it.b = n, append(it.b[:0], b...)
		a.pos = (a.pos + 1) % len(a.ring)
	}
}

func (a *analyzer) finish() {
	res := a.res
	for _, s := range a.sup {
		if s.first != nil && s.total > 1+s.first.Repeat {
			s.first.Total = s.total
		}
	}
	for h, e := range a.spikes {
		e.Total = int(a.seen[h])
	}

	// Lines every baseline had and this run never printed.
	type miss struct {
		order uint32
		text  string
	}
	var missing []miss
	for h, t := range a.m.Templates {
		if int(t.Runs) == a.m.Runs && a.seen[h] == 0 {
			missing = append(missing, miss{t.Order, t.Text})
		}
	}
	slices.SortFunc(missing, func(x, y miss) int {
		if x.order != y.order {
			return int(x.order) - int(y.order)
		}
		return strings.Compare(x.text, y.text)
	})
	res.MissingTotal = len(missing)
	for _, x := range missing[:min(len(missing), maxMissing)] {
		res.Missing = append(res.Missing, x.text)
	}

	// Scoring. Per line: how unusual it is (kind, share of never-seen tokens)
	// and whether it reads like a failure; among failures the earlier one wins,
	// because the first failure is usually the cause and later ones its fallout.
	// A value that every passing run agreed on and that changed *before* the
	// first failure is the kind of silent cause nobody greps for: those blocks
	// rank directly after the top one. One baseline cannot tell a constant from
	// a coincidence, so this needs at least two.
	confidence := 1 - 1/float64(a.m.Runs)
	firstFailure := math.MaxInt
	for _, b := range res.Blocks {
		for _, e := range b.Entries {
			if e.failure && e.Line < firstFailure {
				firstFailure = e.Line
			}
		}
	}
	for _, b := range res.Blocks {
		for _, e := range b.Entries {
			if e.Kind == "" {
				continue
			}
			if e.failure {
				e.Score += 3 + 2*(1-float64(e.Line)/float64(res.Lines))
			}
			if e.Kind == "changed" {
				e.Score += 0.8 * confidence
				if a.m.Runs > 1 && e.Line < firstFailure && firstFailure != math.MaxInt {
					e.Early, b.early = true, true
				}
			}
			e.Score = math.Round(e.Score*100) / 100
			b.Score = max(b.Score, e.Score)
		}
		b.Score = math.Round((b.Score+0.05*float64(min(b.Anomalies, 10)))*100) / 100
	}
	ranked := slices.Clone(res.Blocks)
	var best *Block
	for _, b := range ranked {
		if best == nil || b.Score > best.Score {
			best = b
		}
	}
	tier := func(b *Block) int {
		switch {
		case b == best:
			return 0
		case b.early:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(ranked, func(x, y *Block) int {
		if tx, ty := tier(x), tier(y); tx != ty {
			return tx - ty
		}
		if x.Score != y.Score {
			if x.Score > y.Score {
				return -1
			}
			return 1
		}
		return x.Line - y.Line
	})
	for i, b := range ranked {
		b.Rank = i + 1
	}
}

// clip makes log bytes safe to store and print: bounded length, valid UTF-8,
// and no control characters, so an untrusted log cannot drive the terminal.
func clip(b []byte) string {
	suffix := ""
	if len(b) > maxText {
		b, suffix = b[:maxText], "..."
	}
	s := strings.ToValidUTF8(string(b), "�")
	isCtl := func(r rune) bool { return r < 0x20 && r != '\t' || r >= 0x7f && r <= 0x9f }
	if strings.IndexFunc(s, isCtl) >= 0 {
		s = strings.Map(func(r rune) rune {
			if isCtl(r) {
				return '�'
			}
			return r
		}, s)
	}
	return s + suffix
}
