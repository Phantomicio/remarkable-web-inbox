package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateTokenUses128Bits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "token")
	token, err := loadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 32 {
		t.Fatalf("token length=%d, want 32 hex characters", len(token))
	}
	again, err := loadOrCreateToken(path)
	if err != nil || again != token {
		t.Fatalf("token was not stable: first=%q second=%q err=%v", token, again, err)
	}
}

func TestMakeEPUB(t *testing.T) {
	data, err := makeEPUB("测试标题", "# 标题\n\n正文\n\n```\ncode <x>\n```", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 6 || zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store {
		t.Fatalf("invalid EPUB layout: %#v", zr.File)
	}
	r, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	mime, _ := io.ReadAll(r)
	r.Close()
	if string(mime) != "application/epub+zip" {
		t.Fatalf("invalid mimetype: %q", mime)
	}
	// The EPUB signature entry must not use a trailing ZIP data descriptor.
	if data[6]&0x08 != 0 {
		t.Fatalf("mimetype uses a data descriptor; strict EPUB sniffers reject it")
	}
	css := readZipEntry(t, zr, "OEBPS/style.css")
	if !strings.Contains(css, `font-family:"Noto Sans SC",sans-serif`) || !strings.Contains(css, ".message.user") {
		t.Fatalf("chat reading styles missing: %s", css)
	}
}

func TestChatTranscriptLayout(t *testing.T) {
	content := "用户\n\n请解释一下\n\n---\n\nChatGPT\n\n当然可以。\n\n1. 第一项\n2. 第二项\n\n来源：https://chatgpt.com/share/example"
	got := markdownToXHTML(content)
	for _, want := range []string{`class="message user"`, `<div class="role">你</div>`, `class="message assistant"`, `<ol>`, `class="source"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "<p>---</p>") {
		t.Fatalf("chat separator was rendered: %s", got)
	}
}

func TestLeadingMarkdownTitle(t *testing.T) {
	title, remaining := leadingMarkdownTitle("\n# 一段对话\n\n用户\n\n你好")
	if title != "一段对话" || remaining != "用户\n\n你好" {
		t.Fatalf("title=%q remaining=%q", title, remaining)
	}
}

func readZipEntry(t *testing.T, zr *zip.Reader, name string) string {
	t.Helper()
	for _, file := range zr.File {
		if file.Name != name {
			continue
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	t.Fatalf("missing zip entry %s", name)
	return ""
}

func TestSendImportsEPUB(t *testing.T) {
	var uploaded []byte
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseMultipartForm(maxBody); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if header.Filename != "来自 ChatGPT.epub" {
			t.Fatalf("unexpected filename %q", header.Filename)
		}
		if header.Header.Get("Content-Type") != "application/epub+zip" {
			t.Fatalf("unexpected content type %q", header.Header.Get("Content-Type"))
		}
		uploaded, _ = io.ReadAll(file)
		return &http.Response{StatusCode: http.StatusCreated, Status: "201 Created", Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}, nil
	})}

	s := &server{token: "PAIR", uploadURL: "http://upload.test/upload", client: client}
	payload, _ := json.Marshal(sendRequest{Title: "来自 ChatGPT", Content: "你好，reMarkable"})
	req := httptest.NewRequest(http.MethodPost, "/api/send?key=PAIR", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.send(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := zip.NewReader(bytes.NewReader(uploaded), int64(len(uploaded))); err != nil {
		t.Fatalf("uploaded invalid EPUB: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUploadRejectsOtherFiles(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "bad.txt")
	part.Write([]byte("bad"))
	mw.Close()
	s := &server{token: "PAIR"}
	req := httptest.NewRequest(http.MethodPost, "/api/upload?key=PAIR", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.upload(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPrepareUploadConvertsPNGToPDF(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{R: 20, G: 80, B: 160, A: 255})
	img.Set(1, 0, color.RGBA{R: 255, G: 0, B: 0, A: 128})
	var source bytes.Buffer
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	name, pdf, err := prepareUpload("照片.png", source.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if name != "照片.pdf" {
		t.Fatalf("name=%q", name)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatal("image conversion did not produce a PDF")
	}
}

func TestUploadImportsImageAsPDF(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.White)
	var source bytes.Buffer
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseMultipartForm(maxBody); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if header.Filename != "照片.pdf" || header.Header.Get("Content-Type") != "application/pdf" {
			t.Fatalf("filename=%q content-type=%q", header.Filename, header.Header.Get("Content-Type"))
		}
		pdf, _ := io.ReadAll(file)
		if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) {
			t.Fatal("forwarded image was not converted to PDF")
		}
		return &http.Response{StatusCode: http.StatusCreated, Status: "201 Created", Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}, nil
	})}
	s := &server{token: "PAIR", uploadURL: "http://upload.test/upload", client: client}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "照片.png")
	part.Write(source.Bytes())
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/upload?key=PAIR", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.upload(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPrepareUploadKeepsRMDOC(t *testing.T) {
	source := []byte("native archive")
	name, got, err := prepareUpload("备份.rmdoc", source)
	if err != nil || name != "备份.rmdoc" || !bytes.Equal(got, source) {
		t.Fatalf("name=%q data=%q err=%v", name, got, err)
	}
}

func TestPrepareUploadNormalizesEPUB(t *testing.T) {
	var malformed bytes.Buffer
	zw := zip.NewWriter(&malformed)
	w, _ := zw.Create("OEBPS/content.xhtml")
	w.Write([]byte("<html/>"))
	w, _ = zw.Create("mimetype") // Deliberately second and compressed.
	w.Write([]byte("application/epub+zip"))
	zw.Close()

	name, normalized, err := prepareUpload("book.epub", malformed.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if name != "book.epub" {
		t.Fatalf("name=%q", name)
	}
	zr, err := zip.NewReader(bytes.NewReader(normalized), int64(len(normalized)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 2 || zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store {
		t.Fatalf("EPUB was not normalized: %#v", zr.File)
	}
	if normalized[6]&0x08 != 0 {
		t.Fatal("normalized mimetype uses a trailing data descriptor")
	}
}

func TestCleanFilename(t *testing.T) {
	if got := cleanFilename("../../bad/name.epub"); got != "name.epub" {
		t.Fatalf("got %q", got)
	}
}

func TestUSBUploadURLDetection(t *testing.T) {
	if !isUSBUploadURL("http://10.11.99.1/upload") {
		t.Fatal("device USB importer was not detected")
	}
	if isUSBUploadURL("http://upload.test/upload") {
		t.Fatal("test importer was incorrectly detected as the device USB importer")
	}
}

func TestImageRejectsHugeDimensionsBeforeDecode(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	binary.BigEndian.PutUint32(data[16:20], maxImagePixels+1)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	_, err := imageToPDF(data)
	if err == nil || !strings.Contains(err.Error(), "16 million pixels") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEPUBRejectsExcessiveExpandedSize(t *testing.T) {
	var encoded bytes.Buffer
	zw := zip.NewWriter(&encoded)
	_, err := zw.CreateRaw(&zip.FileHeader{Name: "huge", UncompressedSize64: maxEPUBExpanded + 1, Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = normalizeEPUB(encoded.Bytes())
	if err == nil || !strings.Contains(err.Error(), "expanded content") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEPUBRejectsTooManyEntries(t *testing.T) {
	var encoded bytes.Buffer
	zw := zip.NewWriter(&encoded)
	for i := 0; i <= maxEPUBEntries; i++ {
		if _, err := zw.Create(fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := normalizeEPUB(encoded.Bytes())
	if err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLimitedBuffer(t *testing.T) {
	b := limitedBuffer{limit: 3}
	if _, err := b.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("d")); err == nil {
		t.Fatal("size limit not enforced")
	}
	if b.String() != "abc" {
		t.Fatal("buffer changed after rejection")
	}
}

func TestConcurrentJobsRejected(t *testing.T) {
	s := &server{token: "PAIR"}
	s.workMu.Lock()
	defer s.workMu.Unlock()
	for _, handler := range []http.HandlerFunc{s.send, s.upload, s.importerHealth} {
		req := httptest.NewRequest(http.MethodPost, "/?key=PAIR", nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != 429 || w.Header().Get("Retry-After") == "" {
			t.Fatalf("status=%d", w.Code)
		}
	}
}

func TestTextRequestLimit(t *testing.T) {
	s := &server{token: "PAIR"}
	req := httptest.NewRequest(http.MethodPost, "/?key=PAIR", strings.NewReader(`{"content":"`+strings.Repeat("a", maxTextBody)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.send(w, req)
	if w.Code != 400 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestUploadRejectsMultipleFiles(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for i := 0; i < 2; i++ {
		p, _ := mw.CreateFormFile("file", "book.pdf")
		p.Write([]byte("pdf"))
	}
	mw.Close()
	s := &server{token: "PAIR"}
	req := httptest.NewRequest(http.MethodPost, "/?key=PAIR", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.upload(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "one file") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestMultipartForwardingLength(t *testing.T) {
	s := &server{uploadURL: "http://importer.test/upload", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if r.ContentLength != int64(len(data)) {
			t.Fatalf("length=%d bytes=%d", r.ContentLength, len(data))
		}
		if !bytes.Contains(data, []byte("document payload")) {
			t.Fatal("missing file data")
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}}
	if err := s.postDocument("test.pdf", []byte("document payload")); err != nil {
		t.Fatal(err)
	}
}

func TestUploadStreamsPDFAndCleansTemporaryFile(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	payload := strings.Repeat("p", 2<<20)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "book.pdf")
	io.WriteString(part, payload)
	mw.Close()
	imported := false
	s := &server{token: "PAIR", uploadURL: "http://importer.test/upload", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		files, err := os.ReadDir(tempDir)
		if err != nil || len(files) == 0 {
			t.Fatalf("expected disk-backed upload: %v %v", files, err)
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatal(err)
		}
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(part)
		if err != nil || string(data) != payload || part.FileName() != "book.pdf" {
			t.Fatal("streamed file changed")
		}
		imported = true
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}}
	req := httptest.NewRequest(http.MethodPost, "/?key=PAIR", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.upload(w, req)
	if w.Code != 201 || !imported {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	files, err := os.ReadDir(tempDir)
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary uploads leaked: %v %v", files, err)
	}
}

func TestUnauthenticatedRequestsRejected(t *testing.T) {
	s := &server{token: "PAIR"}
	for _, handler := range []http.HandlerFunc{s.send, s.upload, s.importerHealth} {
		req := httptest.NewRequest(http.MethodPost, "/?key=WRONG", nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != 401 {
			t.Fatalf("status=%d", w.Code)
		}
	}
}
