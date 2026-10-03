package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider's Routing and Stays are picked in its editor and made with its
// Save (01huadalang on Discord: each click saved at once); a Save that
// doesn't carry them keeps them, where the Stays set on the Routing page
// used to be lost at the editor's next Save, which never sent it.
func TestProviderSaveRouting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(action, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body)
		}
	}
	is := func(routing, affinity string) {
		t.Helper()
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		if p.Routing != routing || p.Affinity != affinity {
			t.Fatalf("routing %q, affinity %q; want %q, %q", p.Routing, p.Affinity, routing, affinity)
		}
	}
	const form = `"id":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","models":["sol"]`
	post("save", `{`+form+`,"key":"k","new":true}`)
	is("", "")
	// the editor's picks, made with its Save
	post("save", `{`+form+`,"from":"relay","routing":"rotate","affinity":"session"}`)
	is(provider.Rotate, provider.AffinitySession)
	// a Save that doesn't carry them keeps them
	post("save", `{`+form+`,"from":"relay"}`)
	is(provider.Rotate, provider.AffinitySession)
	// set on their own, from the Routing page, and kept by the next Save
	post("route", `{"id":"relay","routing":"order"}`)
	post("affinity", `{"id":"relay","affinity":"turn"}`)
	is(provider.Ordered, provider.AffinityTurn)
	post("save", `{`+form+`,"from":"relay"}`)
	is(provider.Ordered, provider.AffinityTurn)
	// Smart and Auto picked are "" sent, not left out
	post("save", `{`+form+`,"from":"relay","routing":"","affinity":""}`)
	is("", "")
}
