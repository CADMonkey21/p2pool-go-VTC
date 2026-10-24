package web

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/stratum"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
)

// The API endpoints must not opt into cross-origin reads: any website the
// user visits could otherwise read their node's address and payouts.
func TestAPINoCORSHeader(t *testing.T) {
	d := NewDashboard(work.NewWorkManager(nil, work.NewShareChain(nil)), nil, &stratum.StratumServer{}, time.Now())
	for path, h := range map[string]func(*httptest.ResponseRecorder, string){
		"/api/stats":  func(rec *httptest.ResponseRecorder, p string) { d.ServeHTTP(rec, httptest.NewRequest("GET", p, nil)) },
		"/api/status": func(rec *httptest.ResponseRecorder, p string) { d.ServeStatus(rec, httptest.NewRequest("GET", p, nil)) },
	} {
		rec := httptest.NewRecorder()
		h(rec, path)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if v := rec.Header().Get("Access-Control-Allow-Origin"); v != "" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want none", path, v)
		}
	}
}
