package hindsight_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
)

func newClient(t *testing.T, endpoint string) *kh.Client {
	t.Helper()
	c, err := kh.New(kh.Options{Endpoint: endpoint, Timeout: 2 * time.Second})
	require.NoError(t, err)
	return c
}

func TestNewRefusesAnEmptyEndpoint(t *testing.T) {
	t.Parallel()

	_, err := kh.New(kh.Options{})
	require.Error(t, err)
	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, api.KindInvalid, apiErr.Kind)
}

func TestEndpointIsCanonicalisedAndCarriesNoCredential(t *testing.T) {
	t.Parallel()

	c := newClient(t, "https://user:pw@host:443/v1/default?token=abc/")
	assert.Equal(t, "https://host", c.Endpoint(),
		"an ownership record is a file on disk and must not hold a secret, and a path or query is part of how one deployment addresses a base rather than of which deployment it is")
}

func TestRetainSendsTheThreeFieldsThatCarryTheMostWeight(t *testing.T) {
	t.Parallel()

	// A local server that captures the request body, so the assertion is on what goes
	// on the wire rather than on this package's own conversion.
	var got map[string]any
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		decodeJSON(t, r, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"bank_id":"b","items_count":1,"async":true,"operation_ids":["op-1"]}`))
	})

	c := newClient(t, srv)
	res, err := c.Retain(context.Background(), knowledge.RetainRequest{
		BaseID: "b",
		Items: []knowledge.RetainItem{{
			ID:              "wiki:runbook",
			Content:         "# Runbook\n\nStep one.\n",
			Tags:            []string{"toolbox:origin=authored", "vault:docs"},
			Metadata:        map[string]string{"path": "runbook.md", "commit": "abc"},
			Context:         "toolbox-knowledge",
			EventTime:       knowledge.TimelessTimestamp(),
			UpdateMode:      knowledge.UpdateModeReplace,
			ResolveEntities: false,
		}},
		IdempotencyKey: "key-1",
		Async:          true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Accepted)
	assert.Equal(t, []string{"op-1"}, res.OperationIDs)
	assert.False(t, res.Duplicate)

	items := got["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)

	assert.Equal(t, false, item["resolve_entities"],
		"the backend's default is on, and on resolves a name close to one already in the base to that one — so an authored statement can be attributed to something the author never mentioned")
	assert.Equal(t, "replace", item["update_mode"],
		"the schema carries no default for it, so a generated client omits it and the value must always be sent")
	assert.Equal(t, "unset", item["timestamp"],
		"omitting it means now, which is a claim about when somebody saved a file")
	assert.Equal(t, "wiki:runbook", item["document_id"])
	assert.Equal(t, "toolbox-knowledge", item["context"])
	assert.Equal(t, "# Runbook\n\nStep one.\n", item["content"])
	assert.Equal(t, "key-1", got["operation_id"], "a retry after a lost acknowledgement must not enqueue a duplicate")

	// The nullable pointers must not leak as nulls: a field sent as null is a field the
	// backend has to interpret, and "absent" and "null" are different.
	for _, key := range []string{"strategy", "observation_scopes", "document_tags", "entities"} {
		assert.NotContains(t, item, key, "an unset nullable must be omitted rather than sent as null")
	}
}

func TestRetainSendsADeclaredEventTimeAsGiven(t *testing.T) {
	t.Parallel()

	var got map[string]any
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		decodeJSON(t, r, &got)
		_, _ = w.Write([]byte(`{"success":true,"bank_id":"b","items_count":1,"async":true,"operation_ids":["op"]}`))
	})

	c := newClient(t, srv)
	_, err := c.Retain(context.Background(), knowledge.RetainRequest{
		BaseID: "b",
		Items: []knowledge.RetainItem{{
			ID: "x", Content: "y", EventTime: "2026-01-15T00:00:00Z",
			UpdateMode: knowledge.UpdateModeReplace, Context: "c",
		}},
	})
	require.NoError(t, err)
	item := got["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "2026-01-15T00:00:00Z", item["timestamp"])
}

func TestRetainRefusesAnEventTimeItCannotSend(t *testing.T) {
	t.Parallel()

	c := newClient(t, "http://127.0.0.1:1")
	_, err := c.Retain(context.Background(), knowledge.RetainRequest{
		BaseID: "b",
		Items: []knowledge.RetainItem{{
			ID: "x", Content: "y", EventTime: "last tuesday", UpdateMode: knowledge.UpdateModeReplace,
		}},
	})
	require.Error(t, err)
	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, api.KindInvalid, apiErr.Kind,
		"this is a contradiction in the request rather than something the backend has to reject")
	assert.Contains(t, apiErr.Message, "unset")
}

func TestRetainRefusesAnItemWithNoIdentifier(t *testing.T) {
	t.Parallel()

	c := newClient(t, "http://127.0.0.1:1")
	_, err := c.Retain(context.Background(), knowledge.RetainRequest{
		BaseID: "b", Items: []knowledge.RetainItem{{Content: "x"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "document identifier")
}

func TestADocumentThatIsAlreadyGoneIsTheStateTheCallerWanted(t *testing.T) {
	t.Parallel()

	srv := httptestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"no such document"}`))
	})
	c := newClient(t, srv)
	require.NoError(t, c.DeleteContent(context.Background(), "b", "gone"),
		"reporting this as a failure would make a reconcile over an already-reconciled base fail forever")
}

func TestClassifySeparatesNotDeployedFromNotAnswering(t *testing.T) {
	t.Parallel()

	// Nothing is listening: the backend is not deployed at that address.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := dead.Addr().String()
	require.NoError(t, dead.Close())

	c := newClient(t, "http://"+addr)
	_, rerr := c.Retain(context.Background(), knowledge.RetainRequest{
		BaseID: "b", Items: []knowledge.RetainItem{{ID: "x", Content: "y"}},
	})
	require.Error(t, rerr)
	var apiErr *api.Error
	require.ErrorAs(t, rerr, &apiErr)
	assert.Equal(t, api.KindUnavailable, apiErr.Kind)
	assert.Contains(t, apiErr.Message, "not deployed",
		"a refused connection and a timeout are both unavailable and they have opposite fixes: start the backend, or make it faster")

	// Something is listening and does not answer.
	hang, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer hang.Close()
	// The accepted connections are held, because a connection nothing references is closed
	// immediately and the client would see a reset rather than a wait.
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			conn, aerr := hang.Accept()
			if aerr != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			_ = conn.Close()
		}
	}()
	hc := newClient(t, "http://"+hang.Addr().String())
	_, herr := hc.Retain(context.Background(), knowledge.RetainRequest{
		BaseID: "b", Items: []knowledge.RetainItem{{ID: "x", Content: "y"}},
	})
	require.Error(t, herr)
	require.ErrorAs(t, herr, &apiErr)
	assert.Equal(t, api.KindUnavailable, apiErr.Kind)
	assert.Contains(t, herr.Error(), "did not answer",
		"a deployment is deployed and slow, which is a different problem from not being deployed")
}

func TestClassifyMapsStatusesToKinds(t *testing.T) {
	t.Parallel()

	for status, want := range map[int]api.ErrorKind{
		http.StatusBadRequest:          api.KindInvalid,
		http.StatusUnauthorized:        api.KindDenied,
		http.StatusForbidden:           api.KindDenied,
		http.StatusNotFound:            api.KindNotFound,
		http.StatusConflict:            api.KindAlreadyExists,
		http.StatusUnprocessableEntity: api.KindInvalid,
		http.StatusNotImplemented:      api.KindUnsupported,
		http.StatusServiceUnavailable:  api.KindUnavailable,
		http.StatusGatewayTimeout:      api.KindUnavailable,
		http.StatusInternalServerError: api.KindInternal,
	} {
		srv := httptestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"detail":"no"}`))
		})
		_, err := newClient(t, srv).Retain(context.Background(), knowledge.RetainRequest{
			BaseID: "b", Items: []knowledge.RetainItem{{ID: "x", Content: "y"}},
		})
		require.Error(t, err, "status %d", status)
		var apiErr *api.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, want, apiErr.Kind, "status %d", status)
	}
}

// TestAnEchoedRequestIsNotLogged is the guarantee that matters: a backend that echoes the
// request back in its error does not get the request into this framework's logs.
//
// The test uses a secret-shaped value and a line of somebody's documentation, because those are
// the two things that must not appear.
func TestAnEchoedRequestIsNotLogged(t *testing.T) {
	t.Parallel()

	const secret = "sk-do-not-log-this"
	const prose = "The production database password is hunter2"

	for name, body := range map[string]string{
		"echoed in a field this package does not read": `{"detail":"bad request","received_body":"` + secret + `"}`,
		"not JSON at all":         "400 Bad Request: " + secret + " :: " + prose,
		"a JSON array":            `["` + secret + `"]`,
		"nested arbitrarily deep": `{"errors":[{"error":{"msg":"` + prose + `"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(body))
			})
			_, err := newClient(t, srv).Retain(context.Background(), knowledge.RetainRequest{
				BaseID: "b", Items: []knowledge.RetainItem{{ID: "x", Content: secret}},
			})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), secret,
				"the error body is parsed for a failure reason, not quoted: a truncated echo is still a logged credential")
			assert.NotContains(t, err.Error(), prose)
		})
	}
}

// TestAStructuredDetailIsUsed says what the other test implies: a backend's own `detail` is
// read, because a useful error message is worth having and a structured error message is the one
// thing in a response that is about the failure rather than about the request.
//
// A backend that puts a credential in its own `detail` would still leak it. That is a property of
// the backend, and it is the reason this reads only `detail` and only ever from a JSON body: the
// exposure is bounded to one field the backend chose to describe the failure with.
func TestAStructuredDetailIsUsed(t *testing.T) {
	t.Parallel()

	srv := httptestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"content exceeds the 1 MiB limit","received":"ignored"}`))
	})
	_, err := newClient(t, srv).Retain(context.Background(), knowledge.RetainRequest{
		BaseID: "b", Items: []knowledge.RetainItem{{ID: "x", Content: "y"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "content exceeds the 1 MiB limit",
		"an actionable message is the point of reading detail, and a caller who cannot tell what the backend objected to cannot fix it")
	assert.Contains(t, err.Error(), "ingesting 1 document", "and it says what this framework was doing")
}

func TestClassifyLeavesAnAlreadyClassifiedErrorAlone(t *testing.T) {
	t.Parallel()

	original := &api.Error{Kind: api.KindDenied, Message: "the policy refuses this"}
	got := kh.Classify(original, nil, "doing a thing")
	assert.Same(t, original, got, "a provider that has already decided must not have its decision overwritten by a classifier")
}

func TestClassifyOnNilIsNil(t *testing.T) {
	t.Parallel()

	assert.NoError(t, kh.Classify(nil, nil, "nothing happened"))
}

func TestClassifyHandlesAnUnrecognisedFailureAsInternal(t *testing.T) {
	t.Parallel()

	got := kh.Classify(errors.New("something odd"), nil, "doing a thing")
	var apiErr *api.Error
	require.ErrorAs(t, got, &apiErr)
	assert.Equal(t, api.KindInternal, apiErr.Kind)
}

func TestBundleMetadataIsReadNotGuessed(t *testing.T) {
	t.Parallel()

	index := "---\ntype: \"index\"\n---\n# Pages\n"
	log := "---\ntype: \"log\"\nid: page-1\n---\nhistory\n"
	page := "---\nid: page-1\n---\n# A page\n"
	plain := "# No frontmatter at all\n"

	assert.Equal(t, "index", kh.BundleKind(index))
	assert.Equal(t, "log", kh.BundleKind(log))
	assert.Equal(t, "", kh.BundleKind(page))
	assert.Equal(t, "", kh.BundleKind(plain))

	id, ok := kh.BundlePageID(log)
	assert.True(t, ok)
	assert.Equal(t, "page-1", id)

	_, ok = kh.BundlePageID(plain)
	assert.False(t, ok, "a file the backend did not annotate is still a page, and degrading to that is the safe direction")

	// A file whose frontmatter this package cannot read is not a reason to drop it.
	assert.Equal(t, "", kh.BundleKind("---\nnot: [valid\n---\nbody"))
}

// fullFeatures is a complete capability set. The generated client requires every one of these
// flags to be present, which is itself worth knowing: a build that omits one makes the version
// endpoint unreadable.
const fullFeatures = `{"observations":true,"mcp":true,"worker":true,"bank_config_api":true,"bank_llm_health":true,"file_upload_api":true,"document_export_api":true,"document_import_api":true,"audit_log":true,"llm_trace":true,"store_document_text":true}`

func TestVersionCompatibility(t *testing.T) {
	t.Parallel()

	// The client is pinned to a commit, so nothing in the build says which backend it was
	// written for. A patch difference cannot change the operations used here; a minor one can.
	assertNoDiff := func(got, want string, want2 bool) {
		t.Helper()
		srv := httptestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"api_version":"` + got + `","features":` + fullFeatures + `}`))
		})
		g, w2, matches, err := newClient(t, srv).CheckVersion(context.Background())
		require.NoError(t, err)
		assert.Equal(t, got, g)
		assert.Equal(t, kh.ExpectedAPIVersion, w2)
		assert.Equal(t, want2, matches)
	}
	assertNoDiff("0.10.1", "0.10.1", true)
	assertNoDiff("0.10.4", "0.10.1", true)
	assertNoDiff("0.11.0", "0.10.1", false)
	assertNoDiff("0.9.9", "0.10.1", false)
}

func TestAnAbsentBackendVersionIsNotAMismatch(t *testing.T) {
	t.Parallel()

	srv := httptestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"api_version":"","features":` + fullFeatures + `}`))
	})
	_, _, matches, err := newClient(t, srv).CheckVersion(context.Background())
	require.NoError(t, err)
	assert.True(t, matches, "reporting an absence as a mismatch would train operators to ignore the check")
}

func TestCheckVersionOnAnUnreachableBackendIsAnErrorNotAPanic(t *testing.T) {
	t.Parallel()

	_, _, _, err := newClient(t, "http://127.0.0.1:1").CheckVersion(context.Background())
	require.Error(t, err)
	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, api.KindUnavailable, apiErr.Kind)
}
