package main

import (
	"strings"
	"testing"
)

func TestNewPageConfig(t *testing.T) {
	local, err := newPageConfig("", "", "", "ws://127.0.0.1:7777")
	if err != nil || local.Mode != "local" {
		t.Fatalf("local = %+v, %v", local, err)
	}
	online, err := newPageConfig("https://abc.execute-api.us-west-2.amazonaws.com/", "us-west-2", "abc123", "")
	if err != nil || online.Mode != "online" || online.APIURL != "https://abc.execute-api.us-west-2.amazonaws.com" {
		t.Fatalf("online = %+v, %v", online, err)
	}
	csp := online.contentSecurityPolicy()
	for _, want := range []string{"https://abc.execute-api.us-west-2.amazonaws.com", "https://cognito-idp.us-west-2.amazonaws.com", "script-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q is missing %q", csp, want)
		}
	}
	for _, bad := range [][3]string{
		{"http://abc.example.com", "us-west-2", "abc"},       // not https
		{"https://abc.example.com?x=1", "us-west-2", "abc"},  // query string
		{"https://user@abc.example.com", "us-west-2", "abc"}, // credentials
		{"https://abc.example.com", "us west 2", "abc"},      // bad Region
		{"https://abc.example.com", "us-west-2", "ABC; x"},   // bad client ID
		{"https://abc.example.com", "", ""},                  // only some settings
	} {
		if _, err := newPageConfig(bad[0], bad[1], bad[2], ""); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}
