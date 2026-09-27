// Package hindsight adapts the Hindsight memory backend to the interfaces in
// [github.com/Manu343726/toolbox/pkg/knowledge].
//
// It is a conversion layer, not a reimplementation. The backend's own generated Go client is used
// for the wire, so the model in this process is the model the backend published rather than
// something written from the same description and free to drift from it. What this package adds
// is the part the generated client cannot do: converting the backend's types and — more
// importantly — its failures into the framework's.
//
// Two decisions are worth stating because they are where a naive adapter goes wrong:
//
//   - **Entity resolution is turned off for authored content.** The backend's default is on, and
//     on means a name close to one already in the base may resolve to that one instead of the one
//     the author wrote. The generated client cannot express "off" for a field whose default is
//     true and whose zero value is false without a pointer, and the shipped Obsidian client never
//     sets it at all — so an authored runbook's "Acme Corp" can attach itself to a different
//     "Acme" in the same base. It is set explicitly on every item here.
//   - **"Not deployed" and "not answering" are different failures.** A refused connection means
//     nothing is listening at that address; a timeout means something is and it did not reply.
//     Both are unavailable to a caller and they have opposite fixes, so the distinction is
//     carried in the message rather than collapsed.
//
// The client is pinned to a commit rather than to a release, because the backend's repository
// tags its tools and integrations and not its client subdirectory. Nothing in the build therefore
// says which backend the adapter was written against, so the expected version is recorded in the
// provider's own configuration and compared with the backend's at startup.
package hindsight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	hs "github.com/vectorize-io/hindsight/hindsight-clients/go"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
)

// ExpectedAPIVersion is the backend's HTTP API version this adapter was written against.
//
// It is a claim about a moment, not a constraint: the backend reports its own version and the
// provider compares the two at startup. A mismatch is a warning rather than a refusal, because a
// deployment running a slightly newer backend is a normal state and refusing to serve would be a
// worse answer than serving and saying what might differ.
const ExpectedAPIVersion = "0.10.1"

// Options configures the adapter.
type Options struct {
	// Endpoint is the backend's base URL. Required.
	Endpoint string
	// APIKey authenticates. Empty means an unauthenticated deployment, which the backend
	// supports and which a local instance usually is.
	APIKey string
	// Timeout bounds one request. Zero uses a default.
	Timeout time.Duration
	// UserAgent is sent on every request, so a backend operator can tell which deployment is
	// calling.
	UserAgent string
}

const defaultTimeout = 60 * time.Second

// Client is an adapter over one backend endpoint.
type Client struct {
	api *hs.APIClient
	// endpoint is kept because the ownership record is bound to it, and because the two must
	// agree: a record built for one endpoint must never be used against another.
	endpoint string
}

// New builds an adapter and verifies the endpoint is usable.
//
// It does not make a request. A provider that cannot start is worse than one that starts and
// reports a backend it cannot reach on the first call, and a deployment that mounts the
// subsystem while the backend is down should still be able to list its own configuration.
func New(opts Options) (*Client, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(opts.Endpoint), "/")
	if endpoint == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "the knowledge backend endpoint is required"}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	cfg := hs.NewConfiguration()
	// The generated client carries its base URL in a server list rather than in a
	// `BasePath` field, and the server entry is the only place a path prefix can live. A
	// deployment that mounts the backend under a prefix and is configured without it
	// would otherwise get 404s from a client that reports no configuration problem.
	cfg.Servers = hs.ServerConfigurations{{URL: endpoint}}
	cfg.HTTPClient = &http.Client{Timeout: timeout}
	cfg.UserAgent = opts.UserAgent
	if opts.APIKey != "" {
		cfg.AddDefaultHeader("Authorization", "Bearer "+opts.APIKey)
	}

	return &Client{api: hs.NewAPIClient(cfg), endpoint: endpoint}, nil
}

// Endpoint returns the canonical endpoint the adapter is bound to, which is what an ownership
// record must be bound to as well.
func (c *Client) Endpoint() string { return CanonicalEndpoint(c.endpoint) }

// CanonicalEndpoint reduces a base URL to what an ownership record may store: scheme, host and
// port, with no credentials, path or query.
func CanonicalEndpoint(raw string) string { return knowledge.CanonicalBackendOrigin(raw) }

// Capabilities is what the backend reports it can do, as of the version it is running.
type Capabilities struct {
	// Version is the backend's own API version string.
	Version           string
	Observations      bool
	Mcp               bool
	Worker            bool
	BankConfigAPI     bool
	BankLLMHealth     bool
	FileUploadAPI     bool
	DocumentExportAPI bool
	DocumentImportAPI bool
	AuditLog          bool
	LLMTrace          bool
	// StoreDocumentText reports whether this deployment keeps the original text of a
	// retained document alongside the facts extracted from it.
	//
	// It is worth surfacing rather than assuming in either direction. The backend does
	// not state this default anywhere, and the difference decides a real claim: with it
	// off, a person can be shown a fact but cannot be shown the sentence a colleague
	// wrote, because the words are not in the base at all. A deployment that advertises
	// "we can show you what the runbook actually says" is only true when this is set.
	StoreDocumentText bool
}

// CheckVersion reports the backend's version against the one this adapter expects.
//
// It is called at startup and is deliberately cheap and non-fatal. A deployment on a slightly
// different backend is a normal state, and the useful thing is to say so rather than to refuse.
//
// It is also deliberately tolerant of a response it cannot read. The backend's version endpoint
// reports a dozen required capability flags, and a build that omits any one of them makes this
// call fail — so treating a decode failure as an error would turn an advisory check into an
// outage of the subsystem's startup, over information nothing depends on. An unreadable version
// is reported as unknown.
func (c *Client) CheckVersion(ctx context.Context) (got, want string, matches bool, err error) {
	want = ExpectedAPIVersion
	caps, cerr := c.Capabilities(ctx)
	if cerr != nil {
		// An unreachable backend is a real problem and is reported as one. A response
		// this process cannot parse is not, and is handled below by Capabilities.
		return "", want, false, cerr
	}
	got = strings.TrimSpace(caps.Version)
	if got == "" {
		// Some builds answer the version endpoint without a version. That is not a
		// mismatch, it is an absence, and reporting it as a mismatch would train
		// operators to ignore the check.
		return "", want, true, nil
	}
	return got, want, versionCompatible(got, want), nil
}

// Capabilities reads what the backend reports it can do.
//
// A response this process cannot decode yields an empty Capabilities and no error, because the
// flags are advisory and a caller acts on them by degrading rather than by refusing.
func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	resp, httpResp, err := c.api.MonitoringAPI.GetVersion(ctx).Execute()
	if err != nil {
		if httpResp != nil && httpResp.StatusCode < 300 {
			// The backend answered and this process could not read it. The status is
			// fine, so the backend is deployed; only the shape is unfamiliar.
			return Capabilities{}, nil
		}
		return Capabilities{}, Classify(err, httpResp, "reading the backend version and capabilities")
	}
	if resp == nil {
		return Capabilities{}, nil
	}
	f := resp.GetFeatures()
	return Capabilities{
		Version:           resp.GetApiVersion(),
		Observations:      f.GetObservations(),
		Mcp:               f.GetMcp(),
		Worker:            f.GetWorker(),
		BankConfigAPI:     f.GetBankConfigApi(),
		BankLLMHealth:     f.GetBankLlmHealth(),
		FileUploadAPI:     f.GetFileUploadApi(),
		DocumentExportAPI: f.GetDocumentExportApi(),
		DocumentImportAPI: f.GetDocumentImportApi(),
		AuditLog:          f.GetAuditLog(),
		LLMTrace:          f.GetLlmTrace(),
		StoreDocumentText: f.GetStoreDocumentText(),
	}, nil
}

// versionCompatible compares two dotted versions, tolerating a differing patch.
//
// A patch difference cannot change a contract this adapter uses, and refusing over one would make
// the check noise rather than signal. A differing minor is a real difference and is reported.
func versionCompatible(got, want string) bool {
	g, w := splitVersion(got), splitVersion(want)
	if len(g) == 0 || len(w) == 0 {
		return got == want
	}
	for i := 0; i < len(g) && i < len(w) && i < 1; i++ {
		if g[i] != w[i] {
			return false
		}
	}
	if len(g) > 1 && len(w) > 1 {
		return g[1] == w[1]
	}
	return true
}

func splitVersion(v string) []string {
	parts := strings.Split(strings.TrimSpace(v), ".")
	out := parts[:0]
	for _, p := range parts {
		digits := strings.TrimFunc(p, func(r rune) bool { return r < '0' || r > '9' })
		if digits == "" {
			break
		}
		out = append(out, digits)
	}
	return out
}

// Retain ingests a batch of text content. It implements [knowledge.Engine].
func (c *Client) Retain(ctx context.Context, req knowledge.RetainRequest) (knowledge.RetainResult, error) {
	if len(req.Items) == 0 {
		return knowledge.RetainResult{}, nil
	}
	items := make([]hs.MemoryItem, 0, len(req.Items))
	for _, it := range req.Items {
		m, err := memoryItem(it)
		if err != nil {
			return knowledge.RetainResult{}, err
		}
		items = append(items, m)
	}

	body := hs.RetainRequest{Items: items, Async: boolPtr(req.Async)}
	if req.IdempotencyKey != "" {
		body.OperationId = *hs.NewNullableString(&req.IdempotencyKey)
	}
	resp, httpResp, err := c.api.MemoryAPI.RetainMemories(ctx, req.BaseID).RetainRequest(body).Execute()
	if err != nil {
		return knowledge.RetainResult{}, Classify(err, httpResp, "ingesting "+plural(len(req.Items), "document"))
	}
	return retainResult(resp), nil
}

// memoryItem converts one of this framework's items into the backend's.
//
// The three fields that carry the most weight are the ones with the least obvious default:
//
//   - `resolve_entities` is a *bool in the generated client because the backend's default is true.
//     A zero-valued bool is false, which is the side we want, but relying on a zero value to mean
//     "explicitly off" is exactly the sort of thing a later regeneration flips. It is set.
//   - `update_mode` has **no** default in the backend's schema, only in its prose, so a generated
//     client sees nothing and omits it. It is always sent.
//   - `timestamp` is a one-of over a date-time string, a bare string and null, which is why
//     "unset" is expressible at all. Omitting the field means "now", which is a claim this
//     framework is not willing to make on a document's behalf.
func memoryItem(it knowledge.RetainItem) (hs.MemoryItem, error) {
	if strings.TrimSpace(it.ID) == "" {
		return hs.MemoryItem{}, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a retained item needs a document identifier; without one a re-ingest cannot replace rather than duplicate",
		}
	}
	body := it.Content
	m := hs.MemoryItem{
		Content:  hs.Content{String: &body},
		Tags:     it.Tags,
		Metadata: it.Metadata,
		// The pointer is what makes "explicitly off" expressible at all.
		ResolveEntities: boolPtr(it.ResolveEntities),
		DocumentId:      *hs.NewNullableString(&it.ID),
		UpdateMode:      *hs.NewNullableString(&it.UpdateMode),
	}
	if it.Context != "" {
		m.Context = *hs.NewNullableString(&it.Context)
	}
	switch it.EventTime {
	case "":
		// Omitted means "now" to the backend. That is a claim about when somebody
		// saved a file, so it is never left implicit.
		m.Timestamp = *hs.NewNullableTimestamp(&hs.Timestamp{String: strPtr(knowledge.TimelessTimestamp())})
	case knowledge.TimelessTimestamp():
		m.Timestamp = *hs.NewNullableTimestamp(&hs.Timestamp{String: strPtr("unset")})
	default:
		if _, err := time.Parse(time.RFC3339, it.EventTime); err != nil {
			return hs.MemoryItem{}, &api.Error{
				Kind:    api.KindInvalid,
				Message: fmt.Sprintf("event time %q is not an RFC 3339 timestamp, and %q is the one other value this accepts", it.EventTime, knowledge.TimelessTimestamp()),
			}
		}
		m.Timestamp = *hs.NewNullableTimestamp(&hs.Timestamp{String: strPtr(it.EventTime)})
	}
	return m, nil
}

func retainResult(resp *hs.RetainResponse) knowledge.RetainResult {
	if resp == nil {
		return knowledge.RetainResult{}
	}
	out := knowledge.RetainResult{
		Accepted:     int(resp.GetItemsCount()),
		OperationIDs: append([]string(nil), resp.GetOperationIds()...),
	}
	if out.Accepted == 0 && len(out.OperationIDs) == 0 {
		if id := resp.GetOperationId(); id != "" {
			out.OperationIDs = []string{id}
		}
	}
	// The backend does not label a replay. Its documented behaviour is that re-sending an
	// operation identifier returns the original operation and creates no new work, and a
	// success carrying no new work and no new items is what that looks like. It is a
	// reading of the response rather than a flag the server set, and the doc comment on
	// RetainResult.Duplicate says so.
	out.Duplicate = resp.GetSuccess() && out.Accepted == 0 && len(out.OperationIDs) == 0
	return out
}

// DeleteContent removes a content item. It implements [knowledge.Engine].
//
// It is only ever called for an identifier an ownership record holds, and that is the whole of
// the authorisation: a reconcile that listed the base would also return content nobody owns — a
// retained conversation, a second corpus — and deleting those is indistinguishable from data
// loss.
func (c *Client) DeleteContent(ctx context.Context, baseID, id string) error {
	if strings.TrimSpace(id) == "" {
		return &api.Error{Kind: api.KindInvalid, Message: "cannot remove content with no identifier"}
	}
	_, httpResp, err := c.api.DocumentsAPI.DeleteDocument(ctx, baseID, id).Execute()
	if err != nil {
		// A document that is already gone is the state the caller wanted. Reporting it as a
		// failure would make a reconcile over an already-reconciled base fail forever.
		if httpResp != nil && httpResp.StatusCode == http.StatusNotFound {
			return nil
		}
		return Classify(err, httpResp, "removing document "+id)
	}
	return nil
}

// RetainBinary ingests a non-text file through the backend's binary path. It implements
// [knowledge.Engine].
//
// The backend's own binary ingest takes a file handle, not bytes, so a path is used when there is
// one. A corpus file is on disk by definition, so this is the ordinary case; a caller with bytes
// that are not on disk gets them written to a temporary file, because refusing would be a worse
// answer than a copy.
func (c *Client) RetainBinary(ctx context.Context, req knowledge.BinaryRetainRequest) (knowledge.RetainResult, error) {
	if len(req.Items) == 0 {
		return knowledge.RetainResult{}, nil
	}
	handles := make([]*os.File, 0, len(req.Items))
	cleanups := make([]func(), 0, len(req.Items))
	defer func() {
		for _, c := range cleanups {
			c()
		}
		for _, h := range handles {
			_ = h.Close()
		}
	}()

	for _, it := range req.Items {
		if strings.TrimSpace(it.ID) == "" {
			return knowledge.RetainResult{}, &api.Error{
				Kind:    api.KindInvalid,
				Message: "a retained binary item needs a document identifier",
			}
		}
		name := it.Path
		if name == "" {
			tmp, err := os.CreateTemp("", "toolbox-knowledge-*")
			if err != nil {
				return knowledge.RetainResult{}, &api.Error{
					Kind:    api.KindInternal,
					Message: "staging a binary document for the backend's file-only ingest path",
					Err:     err,
				}
			}
			cleanups = append(cleanups, func() { _ = os.Remove(tmp.Name()) })
			if _, err := tmp.Write(it.Content); err != nil {
				_ = tmp.Close()
				return knowledge.RetainResult{}, &api.Error{
					Kind:    api.KindInternal,
					Message: "staging a binary document for the backend's file-only ingest path",
					Err:     err,
				}
			}
			name = tmp.Name()
		}
		f, err := os.Open(name)
		if err != nil {
			return knowledge.RetainResult{}, &api.Error{
				Kind:    api.KindInvalid,
				Message: "opening " + it.ID + " for the backend's binary ingest",
				Err:     err,
			}
		}
		handles = append(handles, f)
	}

	resp, httpResp, err := c.api.FilesAPI.FileRetain(ctx, req.BaseID).Files(handles).Execute()
	if err != nil {
		return knowledge.RetainResult{}, Classify(err, httpResp, "ingesting "+plural(len(req.Items), "binary document"))
	}
	if resp == nil {
		return knowledge.RetainResult{Accepted: len(req.Items)}, nil
	}
	return knowledge.RetainResult{Accepted: len(req.Items), OperationIDs: append([]string(nil), resp.GetOperationIds()...)}, nil
}

// PageTree returns the backend's page tree, flattened, for the projection revision.
//
// Only identity and staleness are read, because that is all a revision needs and the tree is the
// one call that makes a revision cheap: a page that has not been refreshed since its inputs changed
// says so here, without re-exporting a single page.
func (c *Client) PageTree(ctx context.Context, baseID string) ([]knowledge.PageNode, error) {
	resp, httpResp, err := c.api.KnowledgeBaseAPI.GetKnowledgeBaseTree(ctx, baseID).Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "reading the page tree of base "+baseID)
	}
	var out []knowledge.PageNode
	var walk func(nodes []hs.KnowledgeNode, prefix string)
	walk = func(nodes []hs.KnowledgeNode, prefix string) {
		for _, n := range nodes {
			path := prefix
			if n.GetName() != "" {
				if path == "" {
					path = n.GetName()
				} else {
					path = path + "/" + n.GetName()
				}
			}
			node := knowledge.PageNode{
				ID:       n.GetId(),
				TreePath: path,
				IsStale:  n.GetIsStale(),
			}
			if ts := n.GetTimestamp(); ts != "" {
				if parsed, perr := time.Parse(time.RFC3339, ts); perr == nil {
					node.Timestamp = parsed
				}
			}
			if fs := n.GetLastRefreshFailedAt(); fs != "" {
				if parsed, perr := time.Parse(time.RFC3339, fs); perr == nil {
					node.LastRefreshFailedAt = parsed
				}
			}
			// A directory is a node in the tree and not a page, and counting it as a
			// page would make a base of one page in five folders look like six pages.
			if n.GetKind() != "folder" && n.GetId() != "" {
				out = append(out, node)
			}
			walk(n.GetChildren(), path)
		}
	}
	if resp != nil {
		walk(resp.GetRoots(), "")
	}
	return out, nil
}

// ExportPages returns the backend's own page bundle.
//
// It is reused rather than re-rendered. The bundle already exists, and rendering it a second time
// in this process would be a second translation of one artifact, which is precisely what a
// framework rule exists to prevent. A derived page's canonical form is whatever the backend says
// it is.
func (c *Client) ExportPages(ctx context.Context, baseID string) ([]knowledge.PageFile, error) {
	resp, httpResp, err := c.api.KnowledgeBaseAPI.ExportKnowledgeBase(ctx, baseID).Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "exporting the page tree of base "+baseID)
	}
	if resp == nil {
		return nil, nil
	}
	out := make([]knowledge.PageFile, 0, len(resp.GetFiles()))
	for _, f := range resp.GetFiles() {
		file := knowledge.PageFile{
			Path:    knowledge.NormalizePath(f.GetPath()),
			Content: []byte(f.GetContent()),
		}
		// The bundle's own frontmatter says what each file is, and the index and the
		// logs are identified from it rather than from a filename convention — a
		// convention that would be this framework's invention about the backend's
		// output.
		switch bundleFileKind(f.GetContent()) {
		case "index":
			file.IsIndex = true
		case "log":
			file.IsLog = true
		}
		if id, ok := bundleFileID(f.GetContent()); ok {
			file.PageID = id
		}
		out = append(out, file)
	}
	return out, nil
}

// Classify turns a failure from the generated client into a classified error.
//
// The generated error implements `Error() string` and nothing else: the status code, the raw body
// and the unpacked model are behind methods on a concrete type, so a caller has to assert to
// reach them. Framework rule 12 requires the failure to arrive as an [api.ErrorKind] the
// transport can map, so this function is ours whether or not the client is.
//
// The two cases worth separating are both "unavailable": a connection refused means nothing is
// listening at that address, and a timeout means something is and it did not answer. A deployment
// fixes them in opposite ways — start the backend, or make it faster — and a provider that reports
// both as "unavailable" has thrown the diagnosis away.
func Classify(err error, resp *http.Response, doing string) error {
	if err == nil {
		return nil
	}
	var already *api.Error
	if errors.As(err, &already) {
		return already
	}

	// The generated client returns a *pointer* to its error type. Matching the value type
	// instead would compile, never match, and silently classify every backend failure as
	// internal — which is precisely the collapse rule 12 exists to prevent, reached by a
	// single character.
	var apiErr *hs.GenericOpenAPIError
	if errors.As(err, &apiErr) {
		kind := kindForStatus(statusOf(resp, apiErr))
		return &api.Error{
			Kind:    kind,
			Message: fmt.Sprintf("%s: %s", doing, firstLine(apiErr.Body(), apiErr.Error())),
		}
	}

	// No status: the request never completed. What kind of failure that is depends on whether a
	// connection was attempted at all.
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.As(err, &dnsErr):
		return &api.Error{
			Kind:    api.KindUnavailable,
			Message: doing + ": the backend hostname could not be resolved, so nothing is deployed at that address",
			Err:     err,
		}
	case errors.As(err, &opErr) && errors.Is(err, context.DeadlineExceeded):
		return &api.Error{
			Kind:    api.KindUnavailable,
			Message: doing + ": the backend is deployed and did not answer before the deadline",
			Err:     err,
		}
	case errors.Is(err, context.DeadlineExceeded):
		return &api.Error{
			Kind:    api.KindUnavailable,
			Message: doing + ": the backend is deployed and did not answer before the deadline",
			Err:     err,
		}
	case errors.As(err, &opErr):
		return &api.Error{
			Kind:    api.KindUnavailable,
			Message: doing + ": nothing is listening at the backend address, so it is not deployed there",
			Err:     err,
		}
	case errors.Is(err, context.Canceled):
		return &api.Error{
			Kind:    api.KindUnavailable,
			Message: doing + ": cancelled by the caller",
			Err:     err,
		}
	}
	return &api.Error{Kind: api.KindInternal, Message: doing, Err: err}
}

func statusOf(resp *http.Response, apiErr *hs.GenericOpenAPIError) int {
	if resp != nil {
		return resp.StatusCode
	}
	// The generated error carries no status field at all, so the numeric code is
	// recovered from the message it formats. That is unfortunate, and it is the reason the
	// response is preferred whenever there is one.
	if apiErr != nil {
		if code := extractStatus(apiErr.Error()); code != 0 {
			return code
		}
	}
	return 0
}

func kindForStatus(status int) api.ErrorKind {
	// A success status alongside an error means this process could not read what the
	// backend sent: a model that does not match the response. It is not a bad request and
	// saying so would send a caller to fix their input when their input was fine.
	if status >= 200 && status < 300 {
		return api.KindInternal
	}
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return api.KindInvalid
	case http.StatusUnauthorized, http.StatusForbidden:
		return api.KindDenied
	case http.StatusNotFound:
		return api.KindNotFound
	case http.StatusConflict:
		return api.KindAlreadyExists
	case http.StatusNotImplemented:
		return api.KindUnsupported
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return api.KindUnavailable
	default:
		if status == 0 {
			return api.KindInternal
		}
		if status >= 500 {
			return api.KindInternal
		}
		return api.KindInvalid
	}
}

// firstLine reduces a backend error body to a sentence safe to log.
//
// The body is **not** passed through, truncated or otherwise. A backend that echoes a request in
// its error is a real possibility — this one does for a malformed body — and a truncated echo is
// still a logged credential or a logged line of somebody's documentation. So the body is parsed as
// JSON and only the fields that describe the *failure* are read; a body that is not JSON, or that
// carries no such field, contributes nothing and the message says the backend reported no detail.
//
// The distinction is deliberate: a useful error message is worth having, and a useful error
// message that cannot contain a secret is worth more.
func firstLine(body []byte, fallback string) string {
	if detail := errorDetail(body); detail != "" {
		return truncate(detail)
	}
	// The generated error's own message is the HTTP status line for a rejected request,
	// which carries no payload. It is safe.
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		// Strip anything that looks like a body out of it rather than trusting the
		// format, which is not documented and is a generated string.
		if i := strings.Index(fallback, "{"); i >= 0 {
			fallback = strings.TrimSpace(fallback[:i])
		}
		if fallback != "" {
			return truncate(fallback)
		}
	}
	return "the backend reported a failure with no detail"
}

// errorDetail pulls a human-readable failure reason out of a structured error body.
//
// The keys are the ones the backend's own error model uses. Anything else is ignored: an
// unrecognized shape must not become a log line by default, because the whole reason for
// parsing rather than quoting is that the shape is not this framework's to trust.
func errorDetail(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if !strings.HasPrefix(trimmed, "{") {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return ""
	}
	for _, key := range []string{"detail", "message", "error_description", "title", "error"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func truncate(s string) string {
	const max = 400
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func extractStatus(msg string) int {
	for _, tok := range strings.FieldsFunc(msg, func(r rune) bool {
		return r < '0' || r > '9'
	}) {
		n := 0
		ok := len(tok) == 3
		for _, r := range tok {
			n = n*10 + int(r-'0')
		}
		if ok && n >= 100 && n < 600 {
			return n
		}
	}
	return 0
}

func boolPtr(b bool) *bool { return &b }

func strPtr(s string) *string { return &s }

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
