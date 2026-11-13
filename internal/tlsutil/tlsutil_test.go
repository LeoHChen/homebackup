package tlsutil

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSelfSignedIsStableAndServes(t *testing.T) {
	dir := t.TempDir()
	c1, err := LoadOrCreateSelfSigned(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c2, err := LoadOrCreateSelfSigned(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint(c1)
	if fp != Fingerprint(c2) || len(fp) != 64 {
		t.Fatalf("fingerprint must persist across restarts: %s vs %s", fp, Fingerprint(c2))
	}
	if c1.Leaf.NotAfter.Before(time.Now().AddDate(9, 0, 0)) {
		t.Errorf("certificate expires too soon: %v", c1.Leaf.NotAfter)
	}
	if got := ShortFingerprint(fp); len(got) != 19 {
		t.Errorf("short fingerprint = %q", got)
	}

	// A client that pins the fingerprint can connect; serving works over TLS.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{c1}}
	srv.StartTLS()
	defer srv.Close()

	pinned := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, // replaced by the pin check below, as the apps do
		VerifyConnection: func(cs tls.ConnectionState) error {
			if Fingerprint(tls.Certificate{Leaf: cs.PeerCertificates[0]}) != fp {
				return net.InvalidAddrError("fingerprint mismatch")
			}
			return nil
		},
	}}}
	resp, err := pinned.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
