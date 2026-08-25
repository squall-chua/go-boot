package actuator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/squall-chua/go-boot"
)

// TestPrivateListener covers actuator.addr: every endpoint moves to a listener
// the Actuator binds and owns, nothing is left on the application's mux, and
// the whitelist still decides what exists there.
//
// This test is inside the package because the bound address of a port-zero
// listener is not public API: the Actuator has no Addr method, since a real
// deployment names a real port.
func TestPrivateListener(t *testing.T) {
	app, err := goboot.New(goboot.Config{
		// Otherwise Stop waits the default drain delay out.
		Lifecycle: goboot.LifecycleConfig{DrainDelay: time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := New(Config{Addr: "127.0.0.1:0"}, app)
	mux := http.NewServeMux()
	a.MountOn(mux)
	app.Add(a)
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	// Nothing was registered on the application's own mux.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /livez on the application's mux = %d, want 404", rec.Code)
	}

	base := "http://" + a.ln.Addr().String()
	if code := get(t, base+"/livez"); code != http.StatusOK {
		t.Errorf("GET /livez on the private listener = %d, want 200", code)
	}
	if code := get(t, base+"/actuator/info"); code != http.StatusOK {
		t.Errorf("GET /actuator/info on the private listener = %d, want 200", code)
	}
	// The whitelist applies here too, so a private port is not a way round it.
	if code := get(t, base+"/actuator/metrics"); code != http.StatusNotFound {
		t.Errorf("GET /actuator/metrics on the private listener = %d, want 404", code)
	}
}

func get(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
