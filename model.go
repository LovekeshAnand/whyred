package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
)

const (
	modelVersion = 1
	maxVals      = 4  // a slot with more distinct values in one run is volatile
	maxValLen    = 64 // longer values are never tracked
	maxBaselines = 64 // run membership is a uint64 bitmask
	maxExample   = 300
)

// Slot is one digit-bearing token position of a template.
type Slot struct {
	Vol  bool     `json:"vol,omitempty"`  // value carries no signal
	Vals []string `json:"vals,omitempty"` // every value the baselines used
}

// Template is one distinct line shape seen in the baselines.
type Template struct {
	Text  string `json:"text"` // the line as first printed by a passing run
	Slots []Slot `json:"slots,omitempty"`
	Min   uint32 `json:"min"` // occurrences per run, across runs
	Max   uint32 `json:"max"`
	Runs  uint32 `json:"runs"`  // runs it appeared in
	Order uint32 `json:"order"` // first-appearance rank, to list missing lines in log order

	mask uint64   // bit i set: seen in run i
	toks []uint64 // token hashes, only needed while learning wildcards
}

// Model is everything whyred knows about what a passing run looks like.
type Model struct {
	Version   int                  `json:"version"`
	Runs      int                  `json:"runs"`
	Lines     uint64               `json:"lines"`
	Sources   []string             `json:"sources,omitempty"`
	Masks     []string             `json:"masks,omitempty"`
	Templates map[uint64]*Template `json:"templates"`
	Vocab     map[uint64]bool      `json:"vocab"`   // every token hash seen, for novelty scoring
	Wild      map[uint64]bool      `json:"wild"`    // leave-one-out and prefix keys that match "anything here"
	Learned   int                  `json:"learned"` // wildcards that came from baselines disagreeing
}

func newModel() *Model {
	return &Model{Version: modelVersion, Templates: map[uint64]*Template{}, Vocab: map[uint64]bool{}, Wild: map[uint64]bool{}}
}

func hasVal(vals []string, v []byte) bool {
	for _, s := range vals {
		if s == string(v) {
			return true
		}
	}
	return false
}

// learnRun builds a single-run model from one baseline log.
func learnRun(r io.Reader, masks []*regexp.Regexp) (*Model, error) {
	m := newModel()
	m.Runs = 1
	var sc scanner
	err := eachLine(r, func(line []byte) {
		m.Lines++
		b := applyMasks(sc.cleanLine(line), masks)
		h, ok := sc.scan(b)
		if !ok {
			return
		}
		t := m.Templates[h]
		if t == nil {
			t = &Template{Text: clip(b[:min(len(b), maxExample)]), Order: uint32(len(m.Templates)), Runs: 1}
			for i, tk := range sc.toks {
				t.toks = append(t.toks, tk.h)
				m.Vocab[tk.h] = true
				if tk.rnd && len(sc.toks) >= 3 { // a position that holds generated ids accepts any id
					m.Wild[looKey(h, tk.h, i)] = true
				}
				if tk.slot {
					t.Slots = append(t.Slots, Slot{})
				}
			}
			m.Templates[h] = t
		}
		t.Max++
		si := 0
		for _, tk := range sc.toks {
			if !tk.slot {
				continue
			}
			if si >= len(t.Slots) { // only reachable on a 64-bit hash collision
				break
			}
			s := &t.Slots[si]
			si++
			if s.Vol {
				continue
			}
			v := b[tk.s:tk.e]
			switch {
			case tk.vol || len(v) > maxValLen:
				s.Vol, s.Vals = true, nil
			case hasVal(s.Vals, v):
			case len(s.Vals) == maxVals:
				s.Vol, s.Vals = true, nil
			default:
				s.Vals = append(s.Vals, string(v))
			}
		}
	})
	for _, t := range m.Templates {
		t.Min = t.Max
	}
	return m, err
}

// mergeRuns folds single-run models into one. This is where noise is learned:
// a slot whose value set differs between two passing runs is volatile, and a
// token position where passing runs disagree becomes a wildcard.
func mergeRuns(runs []*Model) *Model {
	m := newModel()
	for i, r := range runs {
		bit := uint64(1) << i
		for h, rt := range r.Templates {
			t := m.Templates[h]
			if t == nil {
				rt.mask = bit
				if i > 0 {
					rt.Min = 0
				}
				m.Templates[h] = rt
				continue
			}
			t.mask |= bit
			t.Runs++
			t.Min, t.Max = min(t.Min, rt.Min), max(t.Max, rt.Max)
			for k := range min(len(t.Slots), len(rt.Slots)) {
				a, b := &t.Slots[k], &rt.Slots[k]
				if !a.Vol && (b.Vol || !sameSet(a.Vals, b.Vals)) {
					a.Vol, a.Vals = true, nil
				}
			}
		}
		for _, t := range m.Templates {
			if t.mask&bit == 0 {
				t.Min = 0
			}
		}
		for h := range r.Vocab {
			m.Vocab[h] = true
		}
		for k := range r.Wild {
			m.Wild[k] = true
		}
		m.Lines += r.Lines
		m.Runs++
	}
	m.learnWildcards()
	return m
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}
	return true
}

// learnWildcards generalises templates that passing runs disagree on.
//
// Token wildcard: two templates identical except for one token that never
// occur in the same run ("using temp dir /tmp/build-aXkQz" vs ".../build-pLmRt")
// make that position match anything.
//
// Prefix wildcard: templates that share their first four or more tokens,
// never co-occur, and together appear in every run ("HEAD is now at
// 3fa9c21 <a different commit message each time>") make the tail free text.
//
// ponytail: O(tokens of run-unstable templates) memory; fine up to millions of
// templates, switch to a two-pass count-min filter if models get bigger.
func (m *Model) learnWildcards() {
	if m.Runs < 2 {
		return
	}
	type group struct {
		masks []uint64 // distinct run masks, capped
		or    uint64
	}
	add := func(g map[uint64]group, k, mask uint64) {
		x := g[k]
		x.or |= mask
		if len(x.masks) < 16 && !slices.Contains(x.masks, mask) {
			x.masks = append(x.masks, mask)
		}
		g[k] = x
	}
	disjoint := func(masks []uint64) bool {
		for i, a := range masks {
			for _, b := range masks[i+1:] {
				if a&b == 0 {
					return true
				}
			}
		}
		return false
	}
	all := ^uint64(0) >> (64 - m.Runs)
	loo, prefix := map[uint64]group{}, map[uint64]group{}
	for h, t := range m.Templates {
		n := len(t.toks)
		if int(t.Runs) == m.Runs || n < 3 {
			continue
		}
		var sum uint64
		for i, th := range t.toks {
			add(loo, looKey(h, th, i), t.mask)
			if i >= minPrefix {
				add(prefix, prefixKey(sum, i), t.mask)
			}
			sum += mix(th, i)
		}
	}
	learn := func(k uint64) {
		if !m.Wild[k] {
			m.Wild[k] = true
			m.Learned++
		}
	}
	for k, g := range loo {
		if disjoint(g.masks) {
			learn(k)
		}
	}
	for k, g := range prefix {
		if g.or == all && disjoint(g.masks) {
			learn(k)
		}
	}
}

func (m *Model) save(path string) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(path)
		}
	}()
	gz := gzip.NewWriter(f)
	if err = json.NewEncoder(gz).Encode(m); err != nil {
		return err
	}
	return gz.Close()
}

func loadModel(path string) (*Model, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: not a whyred model: %w", path, err)
	}
	m := newModel()
	// Cap decompressed size: a model is untrusted input like any other file.
	if err := json.NewDecoder(io.LimitReader(gz, 2<<30)).Decode(m); err != nil {
		return nil, fmt.Errorf("%s: not a whyred model: %w", path, err)
	}
	if m.Version != modelVersion {
		return nil, fmt.Errorf("%s: model version %d, this build reads version %d", path, m.Version, modelVersion)
	}
	if m.Templates == nil || m.Runs < 1 {
		return nil, fmt.Errorf("%s: model is empty", path)
	}
	for h, t := range m.Templates {
		if t == nil {
			delete(m.Templates, h)
		}
	}
	return m, nil
}
