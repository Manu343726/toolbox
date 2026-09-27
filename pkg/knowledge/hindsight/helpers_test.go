package hindsight_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// httptestServer starts a local server that stands in for the backend and returns its URL.
//
// The adapter's tests assert on what goes on the wire rather than on this package's own
// conversion functions, because the wire is the contract the backend actually honours: a
// conversion that is wrong in a way the model types cannot show is exactly the bug worth
// catching here.
func httptestServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	// The content type is set here rather than in each handler: the generated client
	// decides how to decode from it, and a test server that omits it produces a
	// "could not read the response" failure that has nothing to do with what the test
	// is about.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func decodeJSON(t *testing.T, r *http.Request, into any) {
	t.Helper()
	defer r.Body.Close()
	require.NoError(t, json.NewDecoder(r.Body).Decode(into))
}
