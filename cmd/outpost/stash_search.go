package main

// Stash: searching a stash's passages by meaning (cosine over the embeddings)
// and by exact words (BM25), with the two rankings merged by reciprocal rank
// fusion. Plain Go, in memory; nothing to install.

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	bm25K1       = 1.2
	bm25B        = 0.75
	rrfK         = 60.0
	stashMinCos  = 0.35 // a passage this close in meaning is relevant for a chat
	stashMinTerm = 2    // or one that shares this many distinctive words with the question
)

type posting struct {
	doc int32
	tf  uint16
}

// stashIndex is the searchable form of a stash's passages.
type stashIndex struct {
	ps   []Passage
	vecs []float32 // len(ps)*dim; empty while a vector index is not available
	dim  int
	post map[string][]posting
	lens []int
	avg  float64
}

var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by for from has have how i if in into is it its of on or that the their then there these they this to was were what when where which who why will with you your do does did can could should would about after all also any been being both each more most not only other our out over than them so some such too very we he she his her him my me no yes up down off`) {
		stopWords[w] = true
	}
}

// tokenize lowercases s and splits it into words (letters and digits).
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// build prepares the word index for BM25.
func (ix *stashIndex) build() {
	ix.post = map[string][]posting{}
	ix.lens = make([]int, len(ix.ps))
	total := 0
	for i, p := range ix.ps {
		toks := tokenize(p.Text)
		ix.lens[i] = len(toks)
		total += len(toks)
		tf := map[string]int{}
		for _, t := range toks {
			if len(t) >= 2 {
				tf[t]++
			}
		}
		for t, n := range tf {
			ix.post[t] = append(ix.post[t], posting{int32(i), uint16(min(n, 65535))})
		}
	}
	if len(ix.ps) > 0 {
		ix.avg = float64(total) / float64(len(ix.ps))
	}
}

// bm25 scores the passages that contain any of the query's words; terms counts how many different words matched.
func (ix *stashIndex) bm25(query string) (scores map[int]float64, terms map[int]int) {
	scores, terms = map[int]float64{}, map[int]int{}
	n := float64(len(ix.ps))
	seen := map[string]bool{}
	for _, t := range tokenize(query) {
		if len(t) < 2 || stopWords[t] || seen[t] {
			continue
		}
		seen[t] = true
		pl := ix.post[t]
		if len(pl) == 0 {
			continue
		}
		idf := math.Log(1 + (n-float64(len(pl))+0.5)/(float64(len(pl))+0.5))
		for _, p := range pl {
			tf := float64(p.tf)
			dl := float64(ix.lens[p.doc])
			scores[int(p.doc)] += idf * tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*dl/math.Max(ix.avg, 1)))
			terms[int(p.doc)]++
		}
	}
	return
}

// StashHit is one passage found by a search.
type StashHit struct {
	Passage
	Index int     `json:"index"` // place in the stash's passage list
	Cos   float64 `json:"cos"`   // closeness in meaning, 0 to 1 (0 when no vector search was possible)
	BM25  float64 `json:"bm25"`
	Terms int     `json:"terms"` // different question words in the passage
	Score float64 `json:"score"` // relevance for display, 0 to 1
	Stash string  `json:"stash,omitempty"`
}

// search returns the top k passages for query. qvec is the question's embedding (nil: words only).
func (ix *stashIndex) search(query string, qvec []float32, k int) []StashHit {
	if len(ix.ps) == 0 || k <= 0 {
		return nil
	}
	if ix.post == nil {
		ix.build()
	}
	rank := map[int]float64{}
	cos := map[int]float64{}
	if len(qvec) == ix.dim && ix.dim > 0 && len(ix.vecs) == len(ix.ps)*ix.dim {
		type sc struct {
			i int
			s float64
		}
		all := make([]sc, len(ix.ps))
		for i := range ix.ps {
			v := ix.vecs[i*ix.dim : (i+1)*ix.dim]
			var d float64
			for j, x := range qvec {
				d += float64(x) * float64(v[j])
			}
			all[i] = sc{i, d}
			cos[i] = d
		}
		sort.Slice(all, func(a, b int) bool { return all[a].s > all[b].s })
		for r, a := range all[:min(len(all), 50)] {
			rank[a.i] += 1 / (rrfK + float64(r+1))
		}
	}
	bs, terms := ix.bm25(query)
	type sc struct {
		i int
		s float64
	}
	var bl []sc
	for i, s := range bs {
		bl = append(bl, sc{i, s})
	}
	sort.Slice(bl, func(a, b int) bool {
		if bl[a].s != bl[b].s {
			return bl[a].s > bl[b].s
		}
		return bl[a].i < bl[b].i
	})
	maxB := 0.0
	if len(bl) > 0 {
		maxB = bl[0].s
	}
	for r, a := range bl[:min(len(bl), 50)] {
		rank[a.i] += 1 / (rrfK + float64(r+1))
	}
	var ids []int
	for i := range rank {
		ids = append(ids, i)
	}
	sort.Slice(ids, func(a, b int) bool {
		if rank[ids[a]] != rank[ids[b]] {
			return rank[ids[a]] > rank[ids[b]]
		}
		return ids[a] < ids[b]
	})
	var out []StashHit
	for _, i := range ids[:min(len(ids), k)] {
		h := StashHit{Passage: ix.ps[i], Index: i, Cos: math.Max(cos[i], 0), BM25: bs[i], Terms: terms[i]}
		switch {
		case len(cos) > 0:
			h.Score = math.Min(h.Cos, 1)
		case maxB > 0:
			h.Score = bs[i] / maxB
		}
		out = append(out, h)
	}
	return out
}

// relevant says whether a hit is close enough to put in front of a chat answer.
func (h StashHit) relevant() bool { return h.Cos >= stashMinCos || h.Terms >= stashMinTerm }
