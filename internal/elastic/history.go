package elastic

import (
	"context"
	"encoding/json"
	"net/url"
)

// Sources returns the _source of up to size documents of one index, read
// with a match-all search; a missing index is an *Error with Status 404.
func (c *Client) Sources(ctx context.Context, index string, size int) ([]json.RawMessage, error) {
	var out struct {
		Hits struct {
			Hits []struct {
				Source json.RawMessage `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := c.do(ctx, "POST", "/"+url.PathEscape(index)+"/_search", map[string]any{"size": size}, &out); err != nil {
		return nil, err
	}
	docs := make([]json.RawMessage, 0, len(out.Hits.Hits))
	for _, h := range out.Hits.Hits {
		docs = append(docs, h.Source)
	}
	return docs, nil
}
