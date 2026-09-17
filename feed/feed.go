package feed

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Item is a normalised RSS/Atom feed entry.
type Item struct {
	Title       string    `json:"title"`
	Link        string    `json:"link"`
	Description string    `json:"description"`
	ImageURL    string    `json:"imageUrl"`
	Published   time.Time `json:"published"`
	Source      string    `json:"source"` // feed title — used as the display tag
}

// Feed is a parsed channel/feed.
type Feed struct {
	Title string
	Items []Item
}

// -----------------------------------------------------------------------
// RSS 2.0 structures
// -----------------------------------------------------------------------

type rssRoot struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title string    `xml:"title"`
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	PubDate     string    `xml:"pubDate"`
	Enclosure   rssEnclosure `xml:"enclosure"`
	MediaContent []mediaContent `xml:"http://search.yahoo.com/mrss/ content"`
	MediaThumbnail []mediaThumbnail `xml:"http://search.yahoo.com/mrss/ thumbnail"`
}

type rssEnclosure struct {
	URL  string `xml:"url,attr"`
	Type string `xml:"type,attr"`
}

type mediaContent struct {
	URL    string `xml:"url,attr"`
	Medium string `xml:"medium,attr"`
}

type mediaThumbnail struct {
	URL string `xml:"url,attr"`
}

// -----------------------------------------------------------------------
// Atom structures
// -----------------------------------------------------------------------

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title   string     `xml:"title"`
	Links   []atomLink `xml:"link"`
	Summary string     `xml:"summary"`
	Content string     `xml:"content"`
	Updated string     `xml:"updated"`
	Published string   `xml:"published"`
	MediaContent []mediaContent `xml:"http://search.yahoo.com/mrss/ content"`
	MediaThumbnail []mediaThumbnail `xml:"http://search.yahoo.com/mrss/ thumbnail"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

// -----------------------------------------------------------------------
// RDF/RSS 1.0 structures
// -----------------------------------------------------------------------

type rdfRoot struct {
	XMLName  xml.Name    `xml:"http://www.w3.org/1999/02/22-rdf-syntax-ns# RDF"`
	Channel  rdfChannel  `xml:"http://purl.org/rss/1.0/ channel"`
	Items    []rdfItem   `xml:"http://purl.org/rss/1.0/ item"`
}

type rdfChannel struct {
	Title string `xml:"title"`
}

type rdfItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	Date        string `xml:"http://purl.org/dc/elements/1.1/ date"`
}

// -----------------------------------------------------------------------
// Parser
// -----------------------------------------------------------------------

var dateFormats = []string{
	time.RFC1123Z,
	time.RFC1123,
	time.RFC3339,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, f := range dateFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func firstImage(enc rssEnclosure, mc []mediaContent, mt []mediaThumbnail) string {
	for _, m := range mt {
		if m.URL != "" {
			return m.URL
		}
	}
	for _, m := range mc {
		if m.URL != "" && (m.Medium == "image" || m.Medium == "") {
			return m.URL
		}
	}
	if strings.HasPrefix(enc.Type, "image") && enc.URL != "" {
		return enc.URL
	}
	return ""
}

// extractImgSrc pulls the first <img src="..."> from an HTML string.
func extractImgSrc(html string) string {
	lower := strings.ToLower(html)
	idx := strings.Index(lower, "<img")
	if idx == -1 {
		return ""
	}
	rest := html[idx:]
	srcIdx := strings.Index(strings.ToLower(rest), "src=")
	if srcIdx == -1 {
		return ""
	}
	rest = rest[srcIdx+4:]
	quote := rest[0]
	if quote != '"' && quote != '\'' {
		return ""
	}
	rest = rest[1:]
	end := strings.IndexByte(rest, quote)
	if end == -1 {
		return ""
	}
	return rest[:end]
}

// Fetch downloads and parses an RSS/Atom feed from url.
func Fetch(url string, client *http.Client) (*Feed, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RssReader/1.0")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml, */*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return parse(body, url)
}

func parse(data []byte, sourceURL string) (*Feed, error) {
	// Detect format by sniffing the root element name.
	type xmlRoot struct {
		XMLName xml.Name
	}
	var root xmlRoot
	xml.Unmarshal(data, &root) //nolint

	local := strings.ToLower(root.XMLName.Local)
	ns := root.XMLName.Space

	switch {
	case local == "rss":
		return parseRSS(data, sourceURL)
	case local == "feed" && strings.Contains(ns, "atom"):
		return parseAtom(data, sourceURL)
	case local == "rdf":
		return parseRDF(data, sourceURL)
	default:
		// Try RSS first, then Atom.
		if f, err := parseRSS(data, sourceURL); err == nil && len(f.Items) > 0 {
			return f, nil
		}
		return parseAtom(data, sourceURL)
	}
}

func parseRSS(data []byte, _ string) (*Feed, error) {
	var r rssRoot
	if err := xml.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	f := &Feed{Title: strings.TrimSpace(r.Channel.Title)}
	for _, i := range r.Channel.Items {
		img := firstImage(i.Enclosure, i.MediaContent, i.MediaThumbnail)
		if img == "" {
			img = extractImgSrc(i.Description)
		}
		f.Items = append(f.Items, Item{
			Title:       strings.TrimSpace(i.Title),
			Link:        strings.TrimSpace(i.Link),
			Description: strings.TrimSpace(i.Description),
			ImageURL:    img,
			Published:   parseDate(i.PubDate),
		})
	}
	return f, nil
}

func parseAtom(data []byte, _ string) (*Feed, error) {
	var a atomFeed
	if err := xml.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	f := &Feed{Title: strings.TrimSpace(a.Title)}
	for _, e := range a.Entries {
		link := ""
		for _, l := range e.Links {
			if l.Rel == "alternate" || l.Rel == "" {
				link = l.Href
				break
			}
		}
		desc := e.Summary
		if desc == "" {
			desc = e.Content
		}
		img := firstImage(rssEnclosure{}, e.MediaContent, e.MediaThumbnail)
		if img == "" {
			img = extractImgSrc(desc)
		}
		pub := parseDate(e.Published)
		if pub.IsZero() {
			pub = parseDate(e.Updated)
		}
		f.Items = append(f.Items, Item{
			Title:       strings.TrimSpace(e.Title),
			Link:        link,
			Description: strings.TrimSpace(desc),
			ImageURL:    img,
			Published:   pub,
		})
	}
	return f, nil
}

func parseRDF(data []byte, _ string) (*Feed, error) {
	var r rdfRoot
	if err := xml.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	f := &Feed{Title: strings.TrimSpace(r.Channel.Title)}
	for _, i := range r.Items {
		f.Items = append(f.Items, Item{
			Title:       strings.TrimSpace(i.Title),
			Link:        strings.TrimSpace(i.Link),
			Description: strings.TrimSpace(i.Description),
			ImageURL:    extractImgSrc(i.Description),
			Published:   parseDate(i.Date),
		})
	}
	return f, nil
}
