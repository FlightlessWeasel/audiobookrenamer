package metadata

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/strutil"
)

type googleBooksProvider struct {
	http   *Client
	apiKey string
}

func (p *googleBooksProvider) Name() string { return "googlebooks" }

func (p *googleBooksProvider) Search(ctx context.Context, q Query) ([]model.Candidate, error) {
	var qstr string
	if q.Freeform != "" {
		qstr = q.Freeform
	} else {
		var b strings.Builder
		if q.Title != "" {
			b.WriteString(`intitle:` + q.Title)
		}
		if q.Author != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(`inauthor:` + q.Author)
		}
		qstr = b.String()
	}

	u := "https://www.googleapis.com/books/v1/volumes?maxResults=10&printType=books&q=" + url.QueryEscape(qstr)
	if p.apiKey != "" {
		u += "&key=" + url.QueryEscape(p.apiKey)
	}

	var resp struct {
		Items []googleVolume `json:"items"`
	}
	if err := p.http.GetJSON(ctx, p.Name(), u, nil, &resp); err != nil {
		return nil, err
	}

	return collectCandidates(resp.Items, func(it googleVolume) model.Candidate {
		vi := it.VolumeInfo
		c := model.Candidate{
			Provider:   p.Name(),
			ProviderID: it.ID,
			Title:      vi.Title,
			Subtitle:   vi.Subtitle,
			Authors:    vi.Authors,
			Year:       yearPrefix(vi.PublishedDate),
			CoverURL:   httpsURL(strutil.FirstNonEmpty(vi.ImageLinks.Thumbnail, vi.ImageLinks.SmallThumbnail)),
		}
		for _, id := range vi.IndustryIdentifiers {
			if id.Type == "ISBN_13" || (c.ISBN == "" && id.Type == "ISBN_10") {
				c.ISBN = id.Identifier
			}
		}
		return c
	}), nil
}

// googleVolume is one entry of the Google Books /volumes response.
type googleVolume struct {
	ID         string `json:"id"`
	VolumeInfo struct {
		Title               string   `json:"title"`
		Subtitle            string   `json:"subtitle"`
		Authors             []string `json:"authors"`
		PublishedDate       string   `json:"publishedDate"`
		IndustryIdentifiers []struct {
			Type       string `json:"type"`
			Identifier string `json:"identifier"`
		} `json:"industryIdentifiers"`
		ImageLinks struct {
			Thumbnail      string `json:"thumbnail"`
			SmallThumbnail string `json:"smallThumbnail"`
		} `json:"imageLinks"`
	} `json:"volumeInfo"`
}

func yearPrefix(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}

func httpsURL(u string) string {
	if strings.HasPrefix(u, "http://") {
		return "https://" + strings.TrimPrefix(u, "http://")
	}
	return u
}
