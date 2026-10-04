package httputil

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recordingBodyObserver struct {
	calls int
	body  []byte
}

func (o *recordingBodyObserver) ObserveRequestBody(body []byte) {
	o.calls++
	o.body = body
}

func newObservedRequest(t *testing.T, body []byte) (*http.Request, *recordingBodyObserver) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	observer := &recordingBodyObserver{}
	return req.WithContext(WithRequestBodyObserver(req.Context(), observer)), observer
}

func TestReadRequestBodyWithPrealloc_NotifiesObserver(t *testing.T) {
	payload := []byte(`{"model":"claude-sonnet-4-6"}`)
	req, observer := newObservedRequest(t, payload)

	got, err := ReadRequestBodyWithPrealloc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, payload) || !bytes.Equal(observer.body, payload) || observer.calls != 1 {
		t.Fatalf("observer calls=%d body=%q, want 1 / %q", observer.calls, observer.body, payload)
	}
}

func TestReadRequestBodyWithPrealloc_ObserverSeesDecodedBody(t *testing.T) {
	payload := []byte(`{"model":"gzip-model"}`)
	var compressed bytes.Buffer
	gw := gzip.NewWriter(&compressed)
	_, _ = gw.Write(payload)
	_ = gw.Close()

	req, observer := newObservedRequest(t, compressed.Bytes())
	req.Header.Set("Content-Encoding", "gzip")
	if _, err := ReadRequestBodyWithPrealloc(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(observer.body, payload) {
		t.Fatalf("observer body = %q, want decoded %q", observer.body, payload)
	}
}

func TestReadRequestBodyWithPrealloc_ObserverOnPrereadAndLenientPaths(t *testing.T) {
	payload := []byte(`{"model":"preread"}`)
	req, observer := newObservedRequest(t, nil)
	req.Body = NewPrereadBody(payload)

	if _, err := ReadLenientJSONRequestBodyWithPrealloc(req, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(observer.body, payload) {
		t.Fatalf("observer body = %q, want %q", observer.body, payload)
	}
}

func TestReadRequestBodyWithPrealloc_WithoutObserver(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(`{}`)))
	if _, err := ReadRequestBodyWithPrealloc(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if WithRequestBodyObserver(req.Context(), nil) != req.Context() {
		t.Fatal("nil observer must not wrap the context")
	}
}
