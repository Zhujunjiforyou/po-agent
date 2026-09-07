package modelcatalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientListsSortedUniqueModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer test-key" {
			t.Fatalf("authorization = %q", authorization)
		}
		_, _ = fmt.Fprint(w, `{"data":[`+
			`{"id":"z-model"},`+
			`{"id":"a-model","context_window":65536,"default_max_output_tokens":8192},`+
			`{"id":"z-model"}]}`)
	}))
	defer server.Close()

	models, err := (Client{HTTP: server.Client()}).List(context.Background(), Source{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "a-model" || models[1].ID != "z-model" {
		t.Fatalf("models = %#v", models)
	}
	if models[0].EffectiveContextWindow() != 65536 || models[0].DefaultMaxOutputTokens != 8192 {
		t.Fatalf("metadata = %#v", models[0])
	}
}

func TestClientReportsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not available", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := (Client{HTTP: server.Client()}).List(context.Background(), Source{BaseURL: server.URL})
	if err == nil {
		t.Fatal("List() unexpectedly succeeded")
	}
}
