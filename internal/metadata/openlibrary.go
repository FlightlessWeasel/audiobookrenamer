package metadata

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"audiobookrenamer/internal/model"
)

type openLibraryProvider struct {
	http *Client
}

func (p *openLibraryProvider) Name() string { return "openlibrary" }

func (p *openLibraryProvider) Search(ctx context.Context, q Query) ([]model.Candidate, error) {
	params := url.Values{}
	params.Set("limit", "10")
	params.Set("fields", "key,title,subtitle,author_name,first_publish_year,isbn,cover_i")
	if q.Freeform != "" {
		params.Set("q", q.Freeform)
	} else {
		if q.Title != "" {
			params.Set("title", q.Title)
		}
		if q.Author != "" {
			params.Set("author", q.Author)
		}
	}

	u := "https://openlibrary.org/search.json?" + params.Encode()
	var resp struct {
		Docs []openLibraryDoc `json:"docs"`
	}
	if err := p.http.GetJSON(ctx, p.Name(), u, nil, &resp); err != nil {
		return nil, err
	}

	return collectCandidates(resp.Docs, func(d openLibraryDoc) model.Candidate {
		c := model.Candidate{
			Provider:   p.Name(),
			ProviderID: strings.TrimPrefix(d.Key, "/works/"),
			Title:      d.Title,
			Subtitle:   d.Subtitle,
			Authors:    d.AuthorName,
			Year:       d.FirstPublishYear,
		}
		if len(d.ISBN) > 0 {
			c.ISBN = d.ISBN[0]
		}
		if d.CoverI > 0 {
			c.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-M.jpg", d.CoverI)
		}
		return c
	}), nil
}

// openLibraryDoc is one entry of the Open Library /search.json response.
type openLibraryDoc struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	Subtitle         string   `json:"subtitle"`
	AuthorName       []string `json:"author_name"`
	FirstPublishYear int      `json:"first_publish_year"`
	ISBN             []string `json:"isbn"`
	CoverI           int      `json:"cover_i"`
}
