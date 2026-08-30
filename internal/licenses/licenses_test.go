package licenses_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/licenses"
)

func TestBundledOfflineAndRender(t *testing.T) {
	fetcher := licenses.NewFetcher(t.TempDir())
	fetcher.Offline = true
	value, err := fetcher.Get(t.Context(), "MIT")
	if err != nil {
		t.Fatal(err)
	}
	body := licenses.Render(value.Body, "Example Person", 2026)
	if !strings.Contains(body, "Copyright (c) 2026 Example Person") || value.Source != licenses.SourceBundled {
		t.Fatalf("license = %+v\n%s", value, body)
	}
}

func TestNoWriteFetchDoesNotPopulateCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"key":"mpl-2.0","name":"MPL","body":"template"}`))
	}))
	defer server.Close()

	cache := t.TempDir()
	fetcher := licenses.NewFetcher(cache)
	fetcher.BaseURL = server.URL
	fetcher.NoWrite = true
	if _, err := fetcher.Get(t.Context(), "mpl-2.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "mpl-2.0.json")); !os.IsNotExist(err) {
		t.Fatalf("no-write fetch populated cache: %v", err)
	}
}

func TestRemoteThenCacheFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"key":"mpl-2.0","name":"MPL","body":"Copyright [year] [fullname]"}`))
	}))
	defer server.Close()

	cache := t.TempDir()
	fetcher := licenses.NewFetcher(cache)
	fetcher.BaseURL = server.URL
	value, err := fetcher.Get(t.Context(), "mpl")
	if err != nil || value.Source != licenses.SourceRemote {
		t.Fatalf("remote = %+v, %v", value, err)
	}
	fetcher.Offline = true
	value, err = fetcher.Get(t.Context(), "mpl-2.0")
	if err != nil || value.Source != licenses.SourceCache || value.Body == "" {
		t.Fatalf("cache = %+v, %v", value, err)
	}
}

func TestUnknownOfflineListsBundledChoices(t *testing.T) {
	fetcher := licenses.NewFetcher(t.TempDir())
	fetcher.Offline = true
	_, err := fetcher.Get(t.Context(), "proprietary")
	if err == nil || !strings.Contains(err.Error(), "mit") {
		t.Fatalf("error = %v", err)
	}
}
