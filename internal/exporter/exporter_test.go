package exporter

import (
	"github.com/CaliLuke/earwig/internal/spool"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestLoopbackGuard(t *testing.T) {
	if loopback("https://example.com") == nil {
		t.Fatal("allowed remote URL")
	}
	if loopback("http://127.0.0.1:5173") != nil {
		t.Fatal("rejected loopback")
	}
}
func TestJSONDirIdempotentDrain(t *testing.T) {
	s, e := spool.Open(filepath.Join(t.TempDir(), "s.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	_, e = s.DB.Exec(`INSERT INTO turns VALUES('trace','p','session','turn','completed',1,2,'{}','hash',1)`)
	if e != nil {
		t.Fatal(e)
	}
	j := JSONDir{Dir: t.TempDir()}
	if e = Drain(s, j); e != nil {
		t.Fatal(e)
	}
	if e = Drain(s, j); e != nil {
		t.Fatal(e)
	}
	if _, e := httptest.NewServer(http.NotFoundHandler()).Client().Get("http://127.0.0.1"); e == nil {
	}
}
