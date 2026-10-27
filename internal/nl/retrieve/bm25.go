package retrieve

import (
	"math"
	"slices"
)

// BM25 parameters (docs/NL.md).
const (
	k1 = 1.2
	b  = 0.75
)

// bm25 is an in-memory BM25 index over token documents.
type bm25 struct {
	tf    []map[string]int
	lens  []int
	df    map[string]int
	avgdl float64
}

func newBM25(docs [][]string) *bm25 {
	ix := &bm25{tf: make([]map[string]int, len(docs)), lens: make([]int, len(docs)), df: map[string]int{}}
	total := 0
	for i, d := range docs {
		tf := map[string]int{}
		for _, t := range d {
			tf[t]++
		}
		for t := range tf {
			ix.df[t]++
		}
		ix.tf[i], ix.lens[i] = tf, len(d)
		total += len(d)
	}
	if len(docs) > 0 {
		ix.avgdl = float64(total) / float64(len(docs))
	}
	return ix
}

func (ix *bm25) idf(t string) float64 {
	n := float64(ix.df[t])
	N := float64(len(ix.tf))
	return math.Log(1 + (N-n+0.5)/(n+0.5))
}

// score returns every document's score for weighted query terms.
func (ix *bm25) score(query map[string]float64) []float64 {
	out := make([]float64, len(ix.tf))
	if ix.avgdl == 0 {
		return out
	}
	terms := make([]string, 0, len(query))
	for t := range query {
		terms = append(terms, t)
	}
	slices.Sort(terms) // fixed summation order: identical floats every run
	for _, t := range terms {
		if ix.df[t] == 0 {
			continue
		}
		idf := ix.idf(t) * query[t]
		for i, tf := range ix.tf {
			f := float64(tf[t])
			if f == 0 {
				continue
			}
			out[i] += idf * f * (k1 + 1) / (f + k1*(1-b+b*float64(ix.lens[i])/ix.avgdl))
		}
	}
	return out
}

// vocabulary lists the indexed terms, sorted.
func (ix *bm25) vocabulary() []string {
	out := make([]string, 0, len(ix.df))
	for t := range ix.df {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}
