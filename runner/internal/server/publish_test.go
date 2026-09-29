package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func makeBundle(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func publishAPI(t *testing.T) *API {
	t.Helper()
	t.Setenv("BLERG_RUNNER_DATA_DIR", t.TempDir())
	return &API{daemonToken: "tok"}
}

func TestMockupUploadAndServeWithSandboxCSP(t *testing.T) {
	a := publishAPI(t)
	bundle := makeBundle(t, map[string]string{
		"index.html": "<h1>Mock</h1><script>alert(1)</script>",
		"app.js":     "console.log('x')",
	})

	req := httptest.NewRequest("POST", "/api/mockups", bytes.NewReader(bundle))
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	a.HandlePostMockup(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var out struct{ ID, URL string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.URL, "/m/") {
		t.Fatalf("url = %q", out.URL)
	}
	// The id is the only thing standing between the public internet and a
	// published page: 16 random bytes → 32 lowercase hex characters.
	if len(out.ID) != 32 || !validPublishID(out.ID) {
		t.Fatalf("mockup id = %q, want 32 hex chars", out.ID)
	}

	// Serve index (implicit) with sandbox CSP.
	sreq := httptest.NewRequest("GET", "/m/"+out.ID+"/", nil)
	srec := httptest.NewRecorder()
	a.HandleServeMockup(srec, sreq)
	if srec.Code != 200 || !strings.Contains(srec.Body.String(), "<h1>Mock</h1>") {
		t.Fatalf("serve: %d %s", srec.Code, srec.Body)
	}
	if csp := srec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Fatalf("missing sandbox CSP: %q", csp)
	}
}

func TestMockupHostGating(t *testing.T) {
	a := publishAPI(t)
	t.Setenv("BLERG_RUNNER_MOCKUP_HOSTS", "m.lab,m.example.com")
	bundle := makeBundle(t, map[string]string{"index.html": "hi"})
	req := httptest.NewRequest("POST", "/api/mockups", bytes.NewReader(bundle))
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	a.HandlePostMockup(rec, req)
	var out struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)

	// App origin: refused (this is the RCE gate).
	sreq := httptest.NewRequest("GET", "/m/"+out.ID+"/", nil)
	sreq.Host = "runner.example.test"
	srec := httptest.NewRecorder()
	a.HandleServeMockup(srec, sreq)
	if srec.Code != http.StatusNotFound {
		t.Fatalf("app-origin serve must 404, got %d", srec.Code)
	}
	// Mockup host: allowed.
	sreq2 := httptest.NewRequest("GET", "/m/"+out.ID+"/", nil)
	sreq2.Host = "m.lab"
	srec2 := httptest.NewRecorder()
	a.HandleServeMockup(srec2, sreq2)
	if srec2.Code != 200 {
		t.Fatalf("mockup-host serve: %d", srec2.Code)
	}
}

func TestMockupRejectsPathEscape(t *testing.T) {
	a := publishAPI(t)
	bundle := makeBundle(t, map[string]string{"../evil.html": "x"})
	req := httptest.NewRequest("POST", "/api/mockups", bytes.NewReader(bundle))
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	a.HandlePostMockup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("escape bundle accepted: %d", rec.Code)
	}
}

func TestScreenshotValidationAndServe(t *testing.T) {
	a := publishAPI(t)

	// Valid PNG accepted.
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.White)
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, img)
	req := httptest.NewRequest("POST", "/api/screenshots", bytes.NewReader(pngBuf.Bytes()))
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	a.HandlePostScreenshot(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("png upload: %d %s", rec.Code, rec.Body)
	}
	var out struct{ URL string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	// /s/<32 hex>.png — the id length is the guessability budget.
	if id := strings.TrimSuffix(strings.TrimPrefix(out.URL, "/s/"), ".png"); len(id) != 32 || !validPublishID(id) {
		t.Fatalf("screenshot url = %q, want /s/<32 hex>.png", out.URL)
	}
	sreq := httptest.NewRequest("GET", out.URL, nil)
	srec := httptest.NewRecorder()
	a.HandleServeScreenshot(srec, sreq)
	if srec.Code != 200 {
		t.Fatalf("serve: %d", srec.Code)
	}

	// SVG/HTML masquerading as an image is rejected (decode-validate).
	req2 := httptest.NewRequest("POST", "/api/screenshots", strings.NewReader(`<svg onload=alert(1)>`))
	req2.Header.Set("Authorization", "Bearer tok")
	req2.Header.Set("Content-Type", "image/png")
	rec2 := httptest.NewRecorder()
	a.HandlePostScreenshot(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("svg accepted as screenshot: %d", rec2.Code)
	}
}
