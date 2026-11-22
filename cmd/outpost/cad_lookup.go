package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Measurements for a new 3D model, looked up before the model writes the file: the pages linked in
// the request (for example a MakerWorld page of a part to adapt) and a free web search for the
// devices or standard parts it names. Outpost cannot edit an existing mesh, so this is how a
// "modify this model for device X" request becomes a new parametric part with real sizes.
// The numbers are shown on the 3D page with their sources, marked as web numbers to check.

// CadRef is one measurement found on the web.
type CadRef struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source,omitempty"`
}

const (
	cadLookupLinks   = 2    // linked pages read
	cadLookupQueries = 2    // searches run
	cadLookupClip    = 4000 // characters of one source given to the model
	cadMaxRefs       = 12
)

const cadQueryPrompt = "This is a request for a 3D-printable part:\n\n%s\n\n" +
	"Write up to two short web search queries that would find the real measurements in millimetres of the devices, " +
	"products or standard parts the part must fit (for example a router's case size or a rack's panel width). " +
	"One query per line, no numbering, no quotes. If nothing needs looking up, reply with exactly NONE."

const cadRefsPrompt = "This is a request for a 3D-printable part:\n\n%s\n\nBelow are web sources. List the measurements from them " +
	"that the part needs. Reply with only a JSON array like " +
	`[{"name":"Router body","value":"100 × 68 × 24 mm","source":"example.com"}]` +
	". Use only numbers that appear in the sources, give the site name as the source, at most 12 entries. " +
	"Also include one entry named \"Original part\" with a short description when a source is a page about an existing 3D model. " +
	"If the sources have no useful measurements, reply with [].\n\n%s"

// cadSearch and cadFetch are the network steps; tests replace them.
func (c *Controller) cadSearch(ctx context.Context, q string) (string, []string, error) {
	if c.cad.searchStub != nil {
		return c.cad.searchStub(ctx, q)
	}
	res, _, err := c.webSearch(ctx, q)
	if err != nil {
		return "", nil, err
	}
	var urls []string
	for _, r := range res {
		urls = append(urls, r.URL)
	}
	return formatResults(res), urls, nil
}

func (c *Controller) cadFetch(ctx context.Context, u string) (string, error) {
	if c.cad.fetchStub != nil {
		return c.cad.fetchStub(ctx, u)
	}
	// Never internal addresses: a search result is untrusted.
	return fetchPageText(ctx, u, false)
}

func siteName(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return u
	}
	return strings.TrimPrefix(p.Hostname(), "www.")
}

// cadLookup gathers sources and asks the model for the measurements in them.
func (c *Controller) cadLookup(ctx context.Context, rec *CadRec, prompt string, readLinks, search bool) []CadRef {
	var src strings.Builder
	add := func(name, text string) {
		src.WriteString("--- Source: " + name + "\n" + searchClip(text, cadLookupClip) + "\n\n")
	}
	if readLinks {
		for i, u := range extractURLs(prompt) {
			if i >= cadLookupLinks {
				break
			}
			text, err := c.cadFetch(ctx, u)
			if err != nil {
				c.cadLog(rec, "Could not read %s: %s", siteName(u), shortText(err.Error(), 160))
				continue
			}
			c.cadLog(rec, "Read the linked page on %s.", siteName(u))
			add(siteName(u), text)
		}
	}
	if search && ctx.Err() == nil {
		reply, err := c.cadLLM(ctx, rec, []ChatMessage{{Role: "user", Content: fmt.Sprintf(cadQueryPrompt, prompt)}})
		if err != nil {
			c.cadLog(rec, "Could not plan the web look-up: %s", shortText(err.Error(), 160))
		} else {
			for _, q := range cadQueries(reply) {
				res, urls, err := c.cadSearch(ctx, q)
				if err != nil {
					c.cadLog(rec, "Web search failed: %s", shortText(err.Error(), 160))
					break
				}
				c.cadLog(rec, "Searched the web: %s", q)
				add("search results for "+q, res)
				for _, u := range urls { // read the first result that opens
					if text, err := c.cadFetch(ctx, u); err == nil {
						add(siteName(u), text)
						break
					}
				}
			}
		}
	}
	if src.Len() == 0 || ctx.Err() != nil {
		return nil
	}
	reply, err := c.cadLLM(ctx, rec, []ChatMessage{{Role: "user", Content: fmt.Sprintf(cadRefsPrompt, prompt, src.String())}})
	if err != nil {
		c.cadLog(rec, "Could not read measurements from the sources: %s", shortText(err.Error(), 160))
		return nil
	}
	return parseCadRefs(reply)
}

var cadListMark = regexp.MustCompile(`^\s*(\d+[.)]|[-*•])\s*`)

// cadQueries reads the query lines of the model's reply.
func cadQueries(reply string) []string {
	var out []string
	for _, l := range strings.Split(reply, "\n") {
		l = strings.Trim(cadListMark.ReplaceAllString(l, ""), "\"'` ")
		if l == "" || strings.EqualFold(l, "NONE") || strings.HasPrefix(l, "```") || len(l) > 200 {
			continue
		}
		out = append(out, l)
		if len(out) == cadLookupQueries {
			break
		}
	}
	return out
}

// parseCadRefs takes the first JSON array in the reply.
func parseCadRefs(reply string) []CadRef {
	i, j := strings.Index(reply, "["), strings.LastIndex(reply, "]")
	if i < 0 || j <= i {
		return nil
	}
	var refs []CadRef
	if json.Unmarshal([]byte(reply[i:j+1]), &refs) != nil {
		return nil
	}
	out := refs[:0]
	for _, r := range refs {
		r.Name, r.Value, r.Source = shortText(strings.TrimSpace(r.Name), 80), shortText(strings.TrimSpace(r.Value), 120), shortText(strings.TrimSpace(r.Source), 80)
		if r.Name != "" && r.Value != "" {
			out = append(out, r)
		}
		if len(out) == cadMaxRefs {
			break
		}
	}
	return out
}

// cadRefsText is what the model that writes the file is told.
func cadRefsText(refs []CadRef, searched bool) string {
	if len(refs) == 0 {
		if !searched {
			return ""
		}
		return "\n\nNo measurements were found on the web. Use typical sizes, make every size a named parameter at the top of the file, and say in your sentences which sizes are guesses."
	}
	var b strings.Builder
	b.WriteString("\n\nMeasurements found on the web (they may be wrong; the owner's own numbers in the request win). Make each size the part depends on a named parameter at the top of the file:\n")
	for _, r := range refs {
		b.WriteString("- " + r.Name + ": " + r.Value)
		if r.Source != "" {
			b.WriteString(" (" + r.Source + ")")
		}
		b.WriteString("\n")
	}
	return b.String()
}
