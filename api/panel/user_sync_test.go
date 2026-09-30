package panel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Foxtea267/ariNode/conf"
)

func TestEmptyUserListDiffersFromNotModified(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("ETag", "users-1")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"users":[]}`))
			return
		}
		if r.Header.Get("If-None-Match") != "users-1" {
			t.Errorf("missing ETag on next request")
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()
	client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "secret", NodeType: "vless", NodeID: 7})
	if err != nil {
		t.Fatal(err)
	}
	users, err := client.GetUserList()
	if err != nil || users == nil || len(users) != 0 {
		t.Fatalf("expected explicit empty user list, got %v, %v", users, err)
	}
	users, err = client.GetUserList()
	if err != nil || users != nil {
		t.Fatalf("expected nil for 304, got %v, %v", users, err)
	}
}
