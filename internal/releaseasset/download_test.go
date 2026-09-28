package releaseasset

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChecksumsRejectUnsafeManifests(t *testing.T) {
	hash := strings.Repeat("a", 64)
	for _, data := range []string{"", "bad  fleetty", hash + "  ../fleetty", hash + "  a/b", hash + "  a\\b", hash + "  fleetty\n" + hash + "  fleetty\n"} {
		if _, err := Checksums([]byte(data)); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	got, err := Checksums([]byte(strings.ToUpper(hash) + " *fleetty\n"))
	if err != nil || got["fleetty"] != hash {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestBytesLimitsAndHTTPSRedirect(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if chunked {
				w.(http.Flusher).Flush()
			}
			fmt.Fprint(w, "12345")
		}))
		_, err := Bytes(context.Background(), s.Client(), s.URL, 4)
		s.Close()
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("missing size limit: %v", err)
		}
	}
	r, _ := http.NewRequest("GET", "http://example.com", nil)
	if err := NewClient().CheckRedirect(r, nil); err == nil {
		t.Fatal("allowed insecure redirect")
	}
	for _, address := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com?q=a", "https://example.com#x"} {
		if err := ValidateBaseURL(address); err == nil {
			t.Fatalf("accepted %s", address)
		}
	}
}
