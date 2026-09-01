package metadata

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"audiobookrenamer/internal/model"
)

type audibleProvider struct {
	http   *Client
	region string
}

func (p *audibleProvider) Name() string { return "audible" }

// audibleHosts maps a region code to its Audible API host.
var audibleHosts = map[string]string{
	"us": "api.audible.com",
	"ca": "api.audible.ca",
	"uk": "api.audible.co.uk",
	"au": "api.audible.com.au",
	"fr": "api.audible.fr",
	"de": "api.audible.de",
	"it": "api.audible.it",
	"es": "api.audible.es",
	"jp": "api.audible.co.jp",
	"in": "api.audible.in",
}

func (p *audibleProvider) host() string {
	if h, ok := audibleHosts[strings.ToLower(p.region)]; ok {
		return h
	}
	return "api.audible.com"
}

// searchURL is the catalog request Search issues. It is split out so the
// response cache key for a query can be computed identically outside Search.
func (p *audibleProvider) searchURL(q Query) string {
	params := url.Values{}
	params.Set("num_results", "10")
	params.Set("products_sort_by", "Relevance")
	params.Set("response_groups", "contributors,product_desc,product_attrs,media,series")
	params.Set("keywords", q.keywords())
	return "https://" + p.host() + "/1.0/catalog/products?" + params.Encode()
}

func (p *audibleProvider) Search(ctx context.Context, q Query) ([]model.Candidate, error) {
	u := p.searchURL(q)

	var resp struct {
		Products []audibleProduct `json:"products"`
	}
	if err := p.http.GetJSON(ctx, p.Name(), u, nil, &resp); err != nil {
		return nil, err
	}

	return collectCandidates(resp.Products, func(pr audibleProduct) model.Candidate {
		c := model.Candidate{
			Provider:   p.Name(),
			ProviderID: pr.ASIN,
			ASIN:       pr.ASIN,
			Title:      pr.Title,
			Subtitle:   pr.Subtitle,
			Year:       yearPrefix(pr.ReleaseDate),
			CoverURL:   largestImage(pr.ProductImages),
		}
		for _, a := range pr.Authors {
			if a.Name != "" {
				c.Authors = append(c.Authors, a.Name)
			}
		}
		for _, n := range pr.Narrators {
			if n.Name != "" {
				c.Narrators = append(c.Narrators, n.Name)
			}
		}
		if len(pr.Series) > 0 {
			c.Series = pr.Series[0].Title
			c.SeriesIndex = pr.Series[0].Sequence
		}
		return c
	}), nil
}

// audibleProduct is one entry of the /1.0/catalog/products response.
type audibleProduct struct {
	ASIN     string `json:"asin"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Authors  []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Narrators []struct {
		Name string `json:"name"`
	} `json:"narrators"`
	ReleaseDate   string            `json:"release_date"`
	PublisherName string            `json:"publisher_name"`
	ProductImages map[string]string `json:"product_images"`
	Series        []struct {
		Title    string `json:"title"`
		Sequence string `json:"sequence"`
	} `json:"series"`
}

// largestImage returns the URL under the highest numeric key in product_images.
func largestImage(imgs map[string]string) string {
	best, bestKey := "", -1
	for k, v := range imgs {
		n, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		if n > bestKey {
			bestKey, best = n, v
		}
	}
	return best
}
