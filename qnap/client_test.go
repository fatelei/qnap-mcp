package qnap

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"":             "/",
		"Public":       "/Public",
		"/Public":      "/Public",
		" /Public/ ":   "/Public/",
		"share_root":   "share_root",
		"recycle_root": "recycle_root",
	}
	for in, want := range cases {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTrimToJSON(t *testing.T) {
	in := []byte("Content-type: text/html; charset=\"UTF-8\"\n{\"status\":1,\"success\":\"true\"}\n")
	got := string(trimToJSON(in))
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Errorf("trimToJSON returned %q", got)
	}
}

func TestClientLoginAndListShares(t *testing.T) {
	const sid = "testsid123"
	var authUser, authPwd string
	var gotSID string
	var gotNode string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, authLoginPath):
			q := r.URL.Query()
			authUser = q.Get("user")
			authPwd = q.Get("pwd")
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><QDocRoot version="1.0"><authPassed><![CDATA[1]]></authPassed><authSid><![CDATA[` + sid + `]]></authSid></QDocRoot>`))
		case strings.HasSuffix(r.URL.Path, utilRequestPath):
			q := r.URL.Query()
			gotSID = q.Get("sid")
			gotNode = q.Get("node")
			if q.Get("func") != "get_tree" {
				t.Errorf("unexpected func: %q", q.Get("func"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"text":"Public","id":"/Public","cls":"w","iconCls":"folder"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := New(Config{Host: srv.URL, Scheme: "http", User: "admin", Password: "s3cret", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shares, err := c.ListShares(context.Background())
	if err != nil {
		t.Fatalf("ListShares: %v", err)
	}
	if len(shares) != 1 || shares[0].Text != "Public" {
		t.Errorf("unexpected shares: %+v", shares)
	}
	if authUser != "admin" {
		t.Errorf("auth user = %q", authUser)
	}
	wantPwd := base64.StdEncoding.EncodeToString([]byte("s3cret"))
	if authPwd != wantPwd {
		t.Errorf("auth pwd = %q, want %q (url-decoded)", authPwd, wantPwd)
	}
	if gotSID != sid {
		t.Errorf("sid = %q, want %q", gotSID, sid)
	}
	if gotNode != "share_root" {
		t.Errorf("node = %q", gotNode)
	}
}

func TestClientRetryOnStatus4(t *testing.T) {
	const newSID = "newsid456"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, authLoginPath) {
			_, _ = w.Write([]byte(`<QDocRoot><authPassed>1</authPassed><authSid><![CDATA[` + newSID + `]]></authSid></QDocRoot>`))
			return
		}
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"status":4,"success":"true"}`))
			return
		}
		if r.URL.Query().Get("sid") != newSID {
			t.Errorf("retry used wrong sid: %q", r.URL.Query().Get("sid"))
		}
		_, _ = w.Write([]byte(`{"status":1,"success":"true"}`))
	}))
	defer srv.Close()

	c, err := New(Config{Host: srv.URL, Scheme: "http", User: "u", Password: "p", SID: "oldsid", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out, err := c.CreateFolder(context.Background(), "/Public", "test")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if out.Status != 1 {
		t.Errorf("status = %d", out.Status)
	}
	if calls != 2 {
		t.Errorf("utilRequest calls = %d, want 2 (retry)", calls)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct{ host, scheme, want string }{
		{"192.168.1.10:8080", "https", "https://192.168.1.10:8080"},
		{"nas.example.com", "https", "https://nas.example.com"},
		{"nas.example.com", "http", "http://nas.example.com:8080"},
		{"https://nas.example.com/", "http", "https://nas.example.com"},
	}
	for _, tc := range cases {
		got, err := normalizeBaseURL(tc.host, tc.scheme)
		if err != nil {
			t.Errorf("normalizeBaseURL(%q,%q): %v", tc.host, tc.scheme, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeBaseURL(%q,%q) = %q, want %q", tc.host, tc.scheme, got, tc.want)
		}
	}
	_, err := normalizeBaseURL("ftp://nas.example.com", "https")
	if err == nil {
		t.Error("expected error for ftp scheme")
	}
}
