package panel

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Foxtea267/AriNode/conf"
	"github.com/vmihailenco/msgpack/v5"
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

func TestMalformedUserSnapshotCannotAdvanceETagOrRevokeUsers(t *testing.T) {
	emptyPack, err := msgpack.Marshal(UserListBody{Users: []UserInfo{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, contentType, body string
		valid                   bool
	}{
		{"nested", "application/json", `{"error":{"users":[]}}`, false},
		{"truncated", "application/json", `{"users":[]`, false},
		{"null", "application/json", `{"users":null}`, false},
		{"trailing", "application/json", `{"users":[]}{}`, false},
		{"duplicate credential", "application/json", `{"users":[{"id":1,"uuid":"same"},{"id":2,"uuid":"same"}]}`, false},
		{"empty msgpack object", "application/x-msgpack", "80", false},
		{"trailing msgpack", "application/x-msgpack", hex.EncodeToString(emptyPack) + "80", false},
		{"empty msgpack users", "application/x-msgpack", hex.EncodeToString(emptyPack), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(tc.body)
			if tc.contentType == "application/x-msgpack" {
				payload, err = hex.DecodeString(tc.body)
				if err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("ETag", "new")
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			p, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "test", NodeID: 1, NodeType: "vless"})
			if err != nil {
				t.Fatal(err)
			}
			p.userEtag = "old"
			users, err := p.GetUserList()
			if tc.valid {
				if err != nil || users == nil || len(users) != 0 || p.userEtag != "new" {
					t.Fatalf("explicit empty snapshot was not applied: %v %v %s", users, err, p.userEtag)
				}
			} else if err == nil || users != nil || p.userEtag != "old" {
				t.Fatalf("invalid snapshot reached authorization: %v %v %s", users, err, p.userEtag)
			}
		})
	}
}
