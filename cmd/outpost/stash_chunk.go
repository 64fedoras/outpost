package main

// Stash: cutting a resource's text into passages.

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	stashChunkDefault   = 800  // characters per passage
	stashOverlapDefault = 120  // characters repeated at the start of the next passage
	stashChunkMin       = 300  // limits for the Advanced settings
	stashChunkMax       = 2400 // about 600 tokens: well inside the embedding window
)

// pageMarkRe is the "[page N]" line the PDF reader writes before each page.
var pageMarkRe = regexp.MustCompile(`(?m)^\[page (\d+)\]\n?`)

// Passage is one piece of a resource: what is embedded, searched and shown.
type Passage struct {
	Res  string `json:"r"`
	N    int    `json:"n"`           // place within the resource, from 1
	Page int    `json:"p,omitempty"` // PDF page the passage starts on
	Text string `json:"t"`
}

// chunkText cuts text into passages of about size characters that repeat overlap
// characters of the one before. Paragraph breaks are used first, then lines and
// sentences, then words; a passage never starts or ends in the middle of a word.
func chunkText(res, text string, size, overlap int) []Passage {
	if size <= 0 {
		size = stashChunkDefault
	}
	if overlap < 0 || overlap >= size/2 {
		overlap = min(stashOverlapDefault, size/4)
	}
	// Split off the page markers first, remembering the page each stretch of text is on.
	type seg struct {
		text string
		page int
	}
	var segs []seg
	page, last := 0, 0
	for _, m := range pageMarkRe.FindAllStringSubmatchIndex(text, -1) {
		if m[0] > last {
			segs = append(segs, seg{text[last:m[0]], page})
		}
		page, _ = strconv.Atoi(text[m[2]:m[3]])
		last = m[1]
	}
	segs = append(segs, seg{text[last:], page})

	var out []Passage
	for _, sg := range segs {
		for _, piece := range splitPieces(strings.TrimSpace(sg.text), size) {
			out = append(out, Passage{Res: res, Page: sg.page, Text: piece})
		}
	}
	// Merge small pieces up to size and add the overlap.
	var merged []Passage
	for _, p := range out {
		if n := len(merged); n > 0 && merged[n-1].Page == p.Page && utf8.RuneCountInString(merged[n-1].Text)+utf8.RuneCountInString(p.Text)+2 <= size {
			merged[n-1].Text += "\n\n" + p.Text
			continue
		}
		merged = append(merged, p)
	}
	for i := range merged {
		merged[i].N = i + 1
		if i > 0 && overlap > 0 && merged[i-1].Page == merged[i].Page {
			if tail := wordTail(merged[i-1].Text, overlap); tail != "" {
				merged[i].Text = tail + " … " + merged[i].Text
			}
		}
	}
	return merged
}

// wordTail is the last about n characters of s, starting at a word.
func wordTail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return ""
	}
	t := string(r[len(r)-n:])
	if i := strings.IndexAny(t, " \n\t"); i >= 0 && i < len(t)-1 {
		t = t[i+1:]
	}
	return strings.TrimSpace(t)
}

// splitPieces cuts text into pieces of at most size characters, on paragraph, line, sentence or word edges.
func splitPieces(text string, size int) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, para := range regexp.MustCompile(`\n\s*\n`).Split(text, -1) {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if utf8.RuneCountInString(para) <= size {
			out = append(out, para)
			continue
		}
		out = append(out, splitLong(para, size)...)
	}
	return out
}

var sentenceEnd = regexp.MustCompile(`[.!?。]["')\]]?\s+`)

// splitLong cuts one long paragraph, preferring lines, then sentences, then words.
func splitLong(p string, size int) []string {
	var out []string
	cur := ""
	flush := func() {
		if t := strings.TrimSpace(cur); t != "" {
			out = append(out, t)
		}
		cur = ""
	}
	for _, line := range strings.Split(p, "\n") {
		for _, unit := range sentenceUnits(line) {
			if utf8.RuneCountInString(unit) > size {
				flush()
				out = append(out, cutWords(unit, size)...)
				continue
			}
			if cur != "" && utf8.RuneCountInString(cur)+utf8.RuneCountInString(unit)+1 > size {
				flush()
			}
			if cur != "" {
				cur += " "
			}
			cur += unit
		}
		cur += "\n"
	}
	flush()
	return out
}

func sentenceUnits(line string) []string {
	var out []string
	last := 0
	for _, m := range sentenceEnd.FindAllStringIndex(line, -1) {
		out = append(out, strings.TrimSpace(line[last:m[1]]))
		last = m[1]
	}
	if t := strings.TrimSpace(line[last:]); t != "" {
		out = append(out, t)
	}
	return out
}

// cutWords cuts s into pieces of at most size characters at spaces (a word longer than size is cut where it stands).
func cutWords(s string, size int) []string {
	var out []string
	cur := ""
	for _, w := range strings.Fields(s) {
		for utf8.RuneCountInString(w) > size {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			r := []rune(w)
			out = append(out, string(r[:size]))
			w = string(r[size:])
		}
		if cur != "" && utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) > size {
			out = append(out, cur)
			cur = ""
		}
		if cur != "" {
			cur += " "
		}
		cur += w
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
