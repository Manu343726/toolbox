package hindsight

import (
	"context"
	"strings"

	hs "github.com/vectorize-io/hindsight/hindsight-clients/go"
)

// Document is a retained document as this package needs to see it.
type Document struct {
	ID    string
	Title string
	Tags  []string
	// Text is the original content, and is empty unless the deployment retains it.
	//
	// That is a per-base setting whose default the backend does not state, so the field can
	// legitimately be empty for a document that exists. Anything this framework says about
	// quoting a person's original words is therefore conditional on it, and the provider
	// reports the setting at startup rather than assuming either way.
	Text        string
	ContentHash string
	ModTime     string
	Source      string
	// MemoryUnitCount is how many facts the document produced.
	MemoryUnitCount int32
}

// GetDocument reads one retained document.
func (c *Client) GetDocument(ctx context.Context, baseID, id string) (Document, error) {
	resp, httpResp, err := c.api.DocumentsAPI.GetDocument(ctx, baseID, id).Execute()
	if err != nil {
		return Document{}, Classify(err, httpResp, "reading document "+id)
	}
	if resp == nil {
		return Document{}, nil
	}
	doc := Document{
		ID:              resp.GetId(),
		Tags:            resp.GetTags(),
		ModTime:         resp.GetUpdatedAt(),
		MemoryUnitCount: resp.GetMemoryUnitCount(),
	}
	// The original text is a nullable field and is nullable *because* the deployment may
	// not retain it. Reading it as a string and treating empty as "no document" would
	// conflate two very different states.
	if t := resp.GetOriginalText(); strings.TrimSpace(t) != "" {
		doc.Text = t
	}
	doc.ContentHash = strings.TrimSpace(resp.GetContentHash())
	if meta := resp.GetDocumentMetadata(); len(meta) > 0 {
		if src, ok := meta["source"].(string); ok {
			doc.Source = src
		}
	}
	if title, ok := resp.GetDocumentMetadata()["title"].(string); ok {
		doc.Title = title
	}
	return doc, nil
}

// ListDocuments returns a base's retained documents.
func (c *Client) ListDocuments(ctx context.Context, baseID string) ([]Document, error) {
	resp, httpResp, err := c.api.DocumentsAPI.ListDocuments(ctx, baseID).Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "listing the documents of base "+baseID)
	}
	if resp == nil {
		return nil, nil
	}
	out := make([]Document, 0, len(resp.GetItems()))
	for _, item := range resp.GetItems() {
		doc := Document{ID: item.GetId(), Tags: item.GetTags(), ModTime: item.GetUpdatedAt()}
		doc.ContentHash = strings.TrimSpace(item.GetContentHash())
		out = append(out, doc)
	}
	return out, nil
}

// Chunk is one segment of a split document.
type Chunk struct {
	ID    string
	Index int32
	Text  string
	// HasText is false when the deployment does not retain raw text.
	HasText bool
}

// ListDocumentChunks returns the segments a document was split into before extraction.
//
// A chunk is a thing a *retained* document has. An authored file and a derived page are not split
// this way, which is why this is origin-specific in the contract and here.
func (c *Client) ListDocumentChunks(ctx context.Context, baseID, documentID string) ([]Chunk, error) {
	resp, httpResp, err := c.api.DocumentsAPI.ListDocumentChunks(ctx, baseID, documentID).Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "reading the chunks of document "+documentID)
	}
	if resp == nil {
		return nil, nil
	}
	items := resp.GetItems()
	out := make([]Chunk, 0, len(items))
	for _, item := range items {
		chunk := Chunk{ID: item.GetChunkId(), Index: item.GetChunkIndex()}
		if t := item.GetChunkText(); strings.TrimSpace(t) != "" {
			chunk.Text, chunk.HasText = t, true
		}
		out = append(out, chunk)
	}
	return out, nil
}

// Cascade is what a re-extraction cost downstream.
type Cascade struct {
	// FactsReplaced counts the facts the replacement removed. Their identifiers do not come
	// back, so a citation naming one is now dangling — the most expensive consequence of an
	// edit and the least visible.
	FactsReplaced int32
	// ObservationsRederived and PagesRederived count what had to be rebuilt because it was
	// consolidated from those facts.
	ObservationsRederived int32
	PagesRederived        int32
	Notes                 []string
}

// ReprocessDocument re-extracts a document from its source, or reports what that would cost.
//
// The backend's re-extraction is asynchronous and reports an operation rather than a cascade, so
// the cascade is described rather than counted. Counting it here would mean inventing numbers, and
// a reconcile that reported invented numbers about the most expensive operation in the system would
// be worse than one that said what is known and where the rest is observable.
func (c *Client) ReprocessDocument(ctx context.Context, baseID, documentID string, dryRun bool) (Cascade, error) {
	if dryRun {
		return Cascade{Notes: []string{
			"this is a preview: the document was not re-extracted",
			"a re-extraction replaces the document's facts rather than appending to them, so their identifiers change; everything consolidated from the old ones is re-derived and a citation naming one of those identifiers becomes dangling",
			"the backend reports the work as an operation rather than a count, so the cascade is observable through the operations service rather than stated here",
		}}, nil
	}
	doc, err := c.GetDocument(ctx, baseID, documentID)
	if err != nil {
		return Cascade{}, err
	}
	resp, httpResp, err := c.api.DocumentsAPI.ReprocessDocument(ctx, baseID, documentID).Execute()
	if err != nil {
		return Cascade{}, Classify(err, httpResp, "re-extracting document "+documentID)
	}
	var opID string
	var items int32
	if resp != nil {
		opID = strings.TrimSpace(resp.GetOperationId())
		items = resp.GetItemsCount()
	}
	notes := []string{
		fmtCascade(doc.MemoryUnitCount),
	}
	if opID != "" {
		notes = append(notes, "the re-extraction is running as operation "+opID)
	}
	return Cascade{FactsReplaced: items, Notes: notes}, nil
}

func fmtCascade(facts int32) string {
	return "the document previously produced " + itoa32(facts) + " facts, and replacing it deletes and re-extracts them: everything consolidated from the old ones is re-derived, and a citation naming one of the old fact identifiers is now dangling"
}

func itoa32(n int32) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// compile-time use of the generated models, so an import removed by a future regeneration is
// caught at build time rather than at the first call that needed it.
var _ = hs.DocumentResponse{}
