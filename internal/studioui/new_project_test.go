package studioui

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func multipartProjectRequest(t *testing.T, mode, premise, paste string, upload []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fields := map[string]string{
		"title": "Harbor Ghosts", "chapter_title": "The Quiet Bell", "mode": mode, "premise": premise,
		"source_text": paste, "style": "seinen_bw", "format": "manga_rtl", "language": "en", "pages_target": "24",
	}
	for key, value := range fields {
		if err := mw.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if upload != nil {
		part, err := mw.CreateFormFile("source_file", "source.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(upload); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/studio/new/premise", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestParseNewProjectFormModesAndSourceBounds(t *testing.T) {
	for _, tc := range []struct {
		name, mode, premise, paste string
		upload                     []byte
		wantError                  bool
	}{
		{name: "premise", mode: "premise", premise: "A courier follows a drowned road."},
		{name: "premise rejects source", mode: "premise", premise: "A courier follows a drowned road.", paste: "private source", wantError: true},
		{name: "adapt paste", mode: "adapt", paste: "第一話\n港の鐘が鳴る。"},
		{name: "adapt upload exact limit", mode: "adapt", upload: bytes.Repeat([]byte("a"), maxSourceBytes)},
		{name: "adapt needs exactly one", mode: "adapt", paste: "one", upload: []byte("two"), wantError: true},
		{name: "adapt rejects oversize", mode: "adapt", upload: bytes.Repeat([]byte("a"), maxSourceBytes+1), wantError: true},
		{name: "adapt rejects invalid utf8", mode: "adapt", upload: []byte{0xff, 0xfe}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := multipartProjectRequest(t, tc.mode, tc.premise, tc.paste, tc.upload)
			values, errs, err := parseNewProjectForm(httptest.NewRecorder(), req)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if errs.Has() != tc.wantError {
				t.Fatalf("errors = %#v, want error %v", errs, tc.wantError)
			}
			if !tc.wantError && tc.mode == "adapt" && strings.TrimSpace(values.SourceText) == "" {
				t.Fatal("valid adaptation lost its source")
			}
		})
	}
}
