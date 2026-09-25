package api

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMultipartStreamsDiskFileAndJoinsOnClose(t *testing.T) {
	var input bytes.Buffer
	mw := multipart.NewWriter(&input)
	_ = mw.WriteField("model", "alias")
	_ = mw.WriteField("language", "en")
	part, _ := mw.CreateFormFile("file", "speech.wav")
	_, _ = io.Copy(part, strings.NewReader(strings.Repeat("audio", 500000)))
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/v1/audio/transcriptions", &input)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	defer r.MultipartForm.RemoveAll()
	f, err := r.MultipartForm.File["file"][0].Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.(*os.File); !ok {
		t.Fatal("large upload did not spill to disk")
	}
	f.Close()
	body, ct := streamMultipart(context.Background(), r.MultipartForm, "upstream")
	_, params, _ := mime.ParseMediaType(ct)
	result, err := multipart.NewReader(body, params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer result.RemoveAll()
	body.Close()
	if result.Value["model"][0] != "upstream" || result.Value["language"][0] != "en" || result.File["file"][0].Size != 2500000 {
		t.Fatal("multipart fields/content lost")
	}
	// Stop before consuming anything: the blocked producer must still exit.
	body, _ = streamMultipart(context.Background(), r.MultipartForm, "upstream")
	done := make(chan struct{})
	go func() { _ = body.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("multipart producer leaked on early close")
	}
}
