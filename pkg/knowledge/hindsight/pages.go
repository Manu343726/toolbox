package hindsight

import (
	"context"
	"fmt"
	"strings"
	"time"

	hs "github.com/vectorize-io/hindsight/hindsight-clients/go"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
)

// andWord renders the " and " between two refused arguments.
func andWord(present bool) string {
	if present {
		return ""
	}
	return " and"
}

// errInvalid is a caller mistake, stated as one.
func errInvalid(msg string) error { return &api.Error{Kind: api.KindInvalid, Message: msg} }

// notFound is an absent thing, named so the caller can say which.
func notFound(msg string) error { return &api.Error{Kind: api.KindNotFound, Message: msg} }

// Page is a node in the derived page tree, in this framework's vocabulary.
//
// It is a separate type from the projection's `knowledge.PageNode` on purpose. That one carries
// identity and staleness because a revision needs nothing else, and it reads the tree without
// re-exporting a single page — the property that makes a revision cheap. This one carries what a
// person configuring a page needs, which is a different read of the same structure.
type Page struct {
	ID     string
	Kind   string
	Name   string
	Path   string
	Parent string
	// BackingModel is the mental model that maintains a page's body. Empty for a folder,
	// which is what distinguishes the two kinds: a folder is a position and a page is a
	// position with a model behind it.
	BackingModel  string
	Tags          []string
	Trigger       knowledge.Trigger
	IsStale       bool
	Timestamp     time.Time
	RefreshFailed time.Time
	Description   string
}

// ListPages returns the whole page tree, flattened, in path order.
//
// The backend hands back a tree and this returns a list, because a tree is a thing a caller
// reconstructs and a list is a thing a caller filters. Folders are included: a caller asking "what
// pages are there" and getting only pages cannot tell an empty tree from one with no folders in it,
// and the second is the case worth noticing.
func (c *Client) ListPages(ctx context.Context, baseID string) ([]Page, error) {
	resp, httpResp, err := c.api.KnowledgeBaseAPI.GetKnowledgeBaseTree(ctx, baseID).Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "reading the page tree of base "+baseID)
	}
	if resp == nil {
		return nil, nil
	}
	var out []Page
	var walk func(nodes []hs.KnowledgeNode, prefix string)
	walk = func(nodes []hs.KnowledgeNode, prefix string) {
		for _, n := range nodes {
			node := pageFrom(n)
			if node.Name != "" {
				node.Path = node.Name
				if prefix != "" {
					node.Path = prefix + "/" + node.Name
				}
			}
			if node.ID != "" {
				out = append(out, node)
			}
			walk(n.GetChildren(), node.Path)
		}
	}
	walk(resp.GetRoots(), "")
	// Path order rather than backend order: a tree walk gives depth-first, which depends on
	// the order the backend happened to store things in. Sorting makes two identical trees
	// read identically, and a listing that reorders itself between calls is a listing
	// nobody can diff.
	sortPages(out)
	return out, nil
}

func pageFrom(n hs.KnowledgeNode) Page {
	page := Page{
		ID: n.GetId(), Kind: n.GetKind(), Name: n.GetName(), Parent: n.GetParentId(),
		BackingModel: n.GetMentalModelId(), Tags: n.GetTags(),
		IsStale: n.GetIsStale(), Description: n.GetDescription(),
		Timestamp: parseLoose(n.GetTimestamp()), RefreshFailed: parseLoose(n.GetLastRefreshFailedAt()),
	}
	if tr := n.Trigger; tr.IsSet() {
		if value := tr.Get(); value != nil {
			page.Trigger = triggerFrom(*value)
		}
	}
	return page
}

// sortPages orders a flattened tree by path, with a node before its own children.
func sortPages(pages []Page) {
	// Insertion sort: the list is the size of one tree, a tree sort would need a comparator
	// on a type that has none, and the whole list is walked once per call anyway.
	for i := 1; i < len(pages); i++ {
		for j := i; j > 0 && pages[j].Path < pages[j-1].Path; j-- {
			pages[j], pages[j-1] = pages[j-1], pages[j]
		}
	}
}

// GetPage reads one node.
func (c *Client) GetPage(ctx context.Context, baseID, nodeID string) (Page, error) {
	resp, httpResp, err := c.api.KnowledgeBaseAPI.GetKnowledgePage(ctx, baseID, nodeID).Execute()
	if err != nil {
		return Page{}, Classify(err, httpResp, "reading page "+nodeID)
	}
	if resp == nil {
		return Page{}, nil
	}
	// The page read returns a different shape from the tree's node — its own fields, and the
	// body alongside them. The body is dropped on purpose: a page's body is synthesized, and
	// `ListContent` already serves it as a `Content` with a generated origin. Returning it
	// here too would be the same bytes reachable by two translations, which is the one thing
	// a derived document must not have.
	page := Page{
		ID: resp.GetId(), Kind: resp.GetType(), Name: resp.GetName(),
		Description: resp.GetDescription(), Tags: resp.GetTags(),
		Timestamp: parseLoose(resp.GetTimestamp()),
	}
	// A page read names no folder and no model. The tree read does, and a page's place in the
	// tree is what a caller configuring it wants — so it is left to `ListPages` to say, and
	// not invented here.
	return page, nil
}

// CreateFolder creates a folder in the page tree.
//
// A parent that does not exist is not created. The backend would resolve the name against the
// deepest existing folder and put the new one there, which means a typo in a path produces a
// folder in a place the caller did not ask for and nothing reports that it is not where it was
// meant to be. So the parent is checked first.
func (c *Client) CreateFolder(ctx context.Context, baseID, name, parentID string, tags []string, description string) (Page, error) {
	if strings.TrimSpace(name) == "" {
		return Page{}, errInvalid("a folder needs a name")
	}
	// The backend's folder create takes a name and a parent and nothing else — no tags, no
	// description. Both are in the contract because a node in a tree should be describable, and
	// both are refused here rather than dropped.
	//
	// They are refused rather than ignored on purpose. A create that reported success while
	// discarding half its arguments leaves a folder that cannot be described and cannot be
	// found again by the thing it was described with, and nothing in the base would say so.
	if len(tags) > 0 || description != "" {
		return Page{}, &api.Error{
			Kind: api.KindUnsupported,
			Message: fmt.Sprintf(
				"a folder carries only a name and a parent; this build refuses tags%s and description%s rather than accepting them and dropping them",
				andWord(len(tags) > 0), andWord(description != "")),
		}
	}
	if parentID != "" {
		if err := c.requireNode(ctx, baseID, parentID); err != nil {
			return Page{}, err
		}
	}
	body := hs.CreateFolderRequest{Name: name}
	if parentID != "" {
		body.ParentId = *hs.NewNullableString(&parentID)
	}
	resp, httpResp, err := c.api.KnowledgeBaseAPI.CreateKnowledgeFolder(ctx, baseID).CreateFolderRequest(body).Execute()
	if err != nil {
		return Page{}, Classify(err, httpResp, "creating a folder in base "+baseID)
	}
	if resp == nil {
		return Page{}, nil
	}
	return pageFrom(*resp), nil
}

// requireNode refuses a parent that is not there, naming what was looked for.
//
// It costs one read. A page tree is small, the call is a single node, and the alternative is a
// folder created somewhere the caller did not name.
func (c *Client) requireNode(ctx context.Context, baseID, nodeID string) error {
	_, err := c.GetPage(ctx, baseID, nodeID)
	if err != nil {
		return err
	}
	// A node that reads as empty with no error is one the backend does not have, and it has to
	// be caught here: the create would otherwise succeed against a parent that is not there.
	pages, err := c.ListPages(ctx, baseID)
	if err != nil {
		return err
	}
	for _, page := range pages {
		if page.ID == nodeID {
			return nil
		}
	}
	return notFound("no node called " + nodeID + " in base " + baseID)
}

// CreatedPage is what a page create reported.
//
// A page create returns three identifiers and a caller usually wants one of them, so all three
// are named rather than collapsed into a single opaque handle. They are different things: the
// page is a position in a tree, the model is the document behind it, and the operation is the
// asynchronous refresh that will fill it.
type CreatedPage struct {
	PageID      string
	ModelID     string
	OperationID string
}

// CreatePage creates a page: a name, the question it answers, and what it is built from.
//
// The body is not a parameter and cannot be. A page *is* a mental model, and its body is always
// synthesized by a model from the question it is given; the backend's public surface has no
// writable body field anywhere. The trigger is sent in full, resolved against the defaults, so the
// page's inputs are explicit at the moment it is created rather than inferred later from whatever
// the backend decided a default was.
func (c *Client) CreatePage(ctx context.Context, baseID, name, sourceQuery, parentID string, tags []string, trigger *knowledge.Trigger) (CreatedPage, error) {
	if strings.TrimSpace(name) == "" {
		return CreatedPage{}, errInvalid("a page needs a name")
	}
	if strings.TrimSpace(sourceQuery) == "" {
		// The question is what the body is synthesized from. A page with no question is a
		// model with no mission, and it produces an empty document that looks correct.
		return CreatedPage{}, errInvalid("a page needs the question it answers; a page's body is synthesized from it, so a page with no question is a page with no content and no visible error")
	}
	if parentID != "" {
		if err := c.requireNode(ctx, baseID, parentID); err != nil {
			return CreatedPage{}, err
		}
	}
	body := hs.CreatePageRequest{Name: name, SourceQuery: sourceQuery, Tags: tags}
	if parentID != "" {
		body.ParentId = *hs.NewNullableString(&parentID)
	}
	if trigger != nil {
		body.Trigger = *hs.NewNullableMentalModelTriggerInput(triggerInput(*trigger))
	}
	resp, httpResp, err := c.api.KnowledgeBaseAPI.CreateKnowledgePage(ctx, baseID).CreatePageRequest(body).Execute()
	if err != nil {
		return CreatedPage{}, Classify(err, httpResp, "creating a page in base "+baseID)
	}
	if resp == nil {
		return CreatedPage{}, nil
	}
	return CreatedPage{
		PageID:      resp.GetPageId(),
		ModelID:     resp.GetMentalModelId(),
		OperationID: resp.GetOperationId(),
	}, nil
}

// PageUpdate is a partial node change. Name, question and description are three-valued — absent
// leaves them — because a name is not optional and a trigger is not a text field, and conflating
// "not mentioned" with "emptied" would empty a name the caller never meant to touch.
type PageUpdate struct {
	Name        *string
	SourceQuery *string
	Tags        []string
	Trigger     *knowledge.Trigger
	// Description is refused by the update: the backend's node update takes a name, a
	// question, tags and a trigger, and no description. It is carried here so the refusal
	// has somewhere to name what was asked for.
	Description *string
}

// UpdatePageNode renames a node or changes what it is built from.
//
// It never changes a page's body, and there is no field through which it could. A page's body is
// what the system currently believes, and a configuration that could put a different body there
// would make the tree a place a tool writes prose and calls it the system's beliefs.
func (c *Client) UpdatePageNode(ctx context.Context, baseID, nodeID string, update PageUpdate) (Page, error) {
	if update.Description != nil {
		return Page{}, &api.Error{
			Kind:    api.KindUnsupported,
			Message: "a node's description is set when it is created and this build cannot change it afterwards; the backend's node update carries no description field, and accepting one and dropping it would report a change that did not happen",
		}
	}
	body := hs.UpdateNodeRequest{}
	if update.Name != nil {
		body.Name = *hs.NewNullableString(update.Name)
	}
	if update.SourceQuery != nil {
		body.SourceQuery = *hs.NewNullableString(update.SourceQuery)
	}
	if update.Tags != nil {
		body.Tags = update.Tags
	}
	if update.Trigger != nil {
		body.Trigger = *hs.NewNullableMentalModelTriggerInput(triggerInput(*update.Trigger))
	}
	resp, httpResp, err := c.api.KnowledgeBaseAPI.UpdateKnowledgeNode(ctx, baseID, nodeID).UpdateNodeRequest(body).Execute()
	if err != nil {
		return Page{}, Classify(err, httpResp, "updating page "+nodeID)
	}
	if resp == nil {
		return Page{}, nil
	}
	return pageFrom(*resp), nil
}

// DeletePageNode removes a node from the tree.
//
// A folder takes what is under it, and the response cannot enumerate the subtree — the backend does
// not report one. So the caller is told to read what is there first, rather than being handed a
// deletion with no way to know what it cost.
func (c *Client) DeletePageNode(ctx context.Context, baseID, nodeID string) error {
	_, httpResp, err := c.api.KnowledgeBaseAPI.DeleteKnowledgeNode(ctx, baseID, nodeID).Execute()
	if err != nil {
		if httpResp != nil && httpResp.StatusCode == 404 {
			return notFound("no node called " + nodeID + " in base " + baseID)
		}
		return Classify(err, httpResp, "deleting page "+nodeID)
	}
	return nil
}
