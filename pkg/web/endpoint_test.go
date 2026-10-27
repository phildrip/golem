package web

import "testing"

// TestSearchProviderExposesEndpoint keeps the allow_net hook honest: if a
// provider stops reporting its endpoint, web_search silently loses its sandbox
// check.
func TestSearchProviderExposesEndpoint(t *testing.T) {
	p := NewGoogleCSE("key", "cx", "")
	namer, ok := SearchProvider(p).(endpointNamer)
	if !ok {
		t.Fatal("GoogleCSE no longer exposes Endpoint(); web_search would skip allow_net")
	}
	if got := namer.Endpoint(); got != "https://www.googleapis.com/customsearch/v1" {
		t.Fatalf("endpoint = %q", got)
	}
}
