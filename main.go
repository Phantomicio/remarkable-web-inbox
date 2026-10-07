package main

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"html"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
)

const (
	defaultListen   = ":8765"
	defaultUpload   = "http://10.11.99.1/upload"
	maxBody         = 101 << 20
	maxFileBytes    = 100 << 20
	maxTextBody     = 4 << 20
	maxImagePixels  = 16_000_000
	maxEPUBExpanded = 128 << 20
	maxEPUBEntries  = 2000
)

type server struct {
	token     string
	uploadURL string
	client    *http.Client
	workMu    sync.Mutex
}

type sendRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Source  string `json:"source"`
	URL     string `json:"url"`
}

func main() {
	tokenPath := env("WEB_INBOX_TOKEN_FILE", "/home/root/web-inbox/token")
	if len(os.Args) == 2 && os.Args[1] == "--check" {
		data, err := os.ReadFile(tokenPath)
		if err != nil {
			log.Fatal("pairing key is not ready: ", err)
		}
		if err := checkInstallation(strings.TrimSpace(string(data))); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Web Inbox is reachable and the native importer is listening.")
		return
	}
	token, err := loadOrCreateToken(tokenPath)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{
		token:     token,
		uploadURL: env("WEB_INBOX_UPLOAD_URL", defaultUpload),
		client:    &http.Client{Timeout: 45 * time.Second},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.home)
	mux.HandleFunc("/api/send", s.send)
	mux.HandleFunc("/api/upload", s.upload)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("/health/importer", s.importerHealth)
	listen := env("WEB_INBOX_LISTEN", defaultListen)
	log.Printf("Web Inbox listening on %s", listen)
	httpServer := &http.Server{
		Addr: listen, Handler: logRequest(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      3 * time.Minute,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	log.Fatal(httpServer.ListenAndServe())
}

// Tablet memory is limited. Reject concurrent jobs instead of buffering a queue.
func (s *server) beginWork(w http.ResponseWriter) bool {
	if s.workMu.TryLock() {
		return true
	}
	w.Header().Set("Retry-After", "3")
	http.Error(w, "another transfer is in progress; please retry when it finishes", http.StatusTooManyRequests)
	return false
}

func (s *server) importerHealth(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "invalid pairing key", http.StatusUnauthorized)
		return
	}
	if !s.beginWork(w) {
		return
	}
	defer s.workMu.Unlock()
	if isUSBUploadURL(s.uploadURL) && !tcpReady("10.11.99.1:80", 300*time.Millisecond) {
		if err := recoverUSBUploadEndpoint(); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
	}
	w.Write([]byte("ok\n"))
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func loadOrCreateToken(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
		return strings.TrimSpace(string(data)), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	// The pairing key is a local-network password. Existing shorter keys remain
	// valid after upgrades, while new installations receive a 128-bit key.
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := strings.ToUpper(hex.EncodeToString(raw))
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		return "", err
	}
	return token, nil
}

func (s *server) authorized(r *http.Request) bool {
	key := r.URL.Query().Get("key")
	if key == "" {
		key = r.Header.Get("X-Web-Inbox-Key")
	}
	if key == "" {
		if c, err := r.Cookie("web_inbox_key"); err == nil {
			key = c.Value
		}
	}
	return subtle.ConstantTimeCompare([]byte(key), []byte(s.token)) == 1
}

func (s *server) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if key := r.URL.Query().Get("key"); key == s.token {
		http.SetCookie(w, &http.Cookie{Name: "web_inbox_key", Value: key, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 31536000})
	}
	if !s.authorized(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, loginHTML)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, homeHTML)
}

func (s *server) send(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "invalid pairing key", http.StatusUnauthorized)
		return
	}
	if !s.beginWork(w) {
		return
	}
	defer s.workMu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, maxTextBody)
	var req sendRequest
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	} else {
		var err error
		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			err = r.ParseMultipartForm(1 << 20)
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
		} else {
			err = r.ParseForm()
		}
		if err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		req.Title = r.FormValue("title")
		req.Content = r.FormValue("content")
		req.Source = r.FormValue("source")
		req.URL = r.FormValue("url")
	}
	if strings.TrimSpace(req.Content) == "" && strings.TrimSpace(req.URL) != "" {
		articleTitle, articleText, err := s.fetchArticle(req.URL)
		if err != nil {
			http.Error(w, "could not extract webpage: "+err.Error(), http.StatusBadGateway)
			return
		}
		req.Content = articleText
		if strings.TrimSpace(req.Title) == "" {
			req.Title = articleTitle
		}
		if strings.TrimSpace(req.Source) == "" {
			req.Source = req.URL
		}
	}
	if strings.TrimSpace(req.Title) == "" {
		if inferredTitle, remaining := leadingMarkdownTitle(req.Content); inferredTitle != "" {
			req.Title = inferredTitle
			req.Content = remaining
		}
	}
	req.Title = cleanTitle(req.Title)
	if req.Title == "" {
		req.Title = "Web Inbox " + time.Now().Format("2006-01-02 15:04")
	}
	if strings.TrimSpace(req.Content) == "" {
		http.Error(w, "content is empty", http.StatusBadRequest)
		return
	}
	epub, err := makeEPUB(req.Title, req.Content, req.Source)
	if err != nil {
		http.Error(w, "could not build EPUB: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.importDocument(req.Title+".epub", epub); err != nil {
		log.Printf("import failed: %v", err)
		http.Error(w, "reMarkable import failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	respondJSON(w, http.StatusCreated, map[string]string{"status": "imported", "title": req.Title})
}

func (s *server) fetchArticle(rawURL string) (string, string, error) {
	pageURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (pageURL.Scheme != "http" && pageURL.Scheme != "https") || pageURL.Hostname() == "" || pageURL.User != nil {
		return "", "", fmt.Errorf("invalid http/https URL")
	}
	if strings.EqualFold(pageURL.Hostname(), "chatgpt.com") && strings.HasPrefix(pageURL.Path, "/share/") {
		return "", "", fmt.Errorf("ChatGPT 分享页会拒绝平板直连；请在 iPhone Safari 打开分享页后运行“发送网页到 reMarkable”快捷指令，或发送 PDF")
	}
	client := newPublicWebClient()
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, pageURL.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; reMarkable Web Inbox) AppleWebKit/537.36 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("webpage returned %s", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "html") {
		return "", "", fmt.Errorf("URL is not an HTML page")
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return "", "", err
	}
	if len(page) > 8<<20 {
		return "", "", fmt.Errorf("webpage exceeds 8 MiB")
	}
	article, err := readability.FromReader(bytes.NewReader(page), pageURL)
	if err != nil {
		return "", "", err
	}
	var text bytes.Buffer
	if err := article.RenderText(&text); err != nil {
		return "", "", err
	}
	content := strings.TrimSpace(text.String())
	if content == "" {
		return "", "", fmt.Errorf("no readable article content found")
	}
	if byline := strings.TrimSpace(article.Byline()); byline != "" {
		content = "作者：" + byline + "\n\n" + content
	}
	return article.Title(), content, nil
}

func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "invalid pairing key", http.StatusUnauthorized)
		return
	}
	if !s.beginWork(w) {
		return
	}
	defer s.workMu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	err := r.ParseMultipartForm(1 << 20)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err != nil {
		http.Error(w, "invalid upload", http.StatusBadRequest)
		return
	}
	if len(r.MultipartForm.File["file"]) != 1 {
		http.Error(w, "send one file at a time", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	if header.Size > maxFileBytes {
		http.Error(w, "file exceeds 100 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	// PDFs and native archives need no conversion. Stream the temporary upload
	// into the importer instead of copying a potentially 100 MiB file into RAM.
	name := cleanFilename(header.Filename)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".pdf" || ext == ".rmdoc" {
		if err := s.importReader(name, file, header.Size); err != nil {
			http.Error(w, "reMarkable import failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		respondJSON(w, http.StatusCreated, map[string]string{"status": "imported", "title": name})
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		http.Error(w, "could not read upload", http.StatusBadRequest)
		return
	}
	if len(data) > maxFileBytes {
		http.Error(w, "file exceeds 100 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	name, data, err = prepareUpload(header.Filename, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.importDocument(name, data); err != nil {
		log.Printf("file import failed for %q: %v", name, err)
		http.Error(w, "reMarkable import failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	respondJSON(w, http.StatusCreated, map[string]string{"status": "imported", "title": name})
}

func prepareUpload(name string, data []byte) (string, []byte, error) {
	name = cleanFilename(name)
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".pdf", ".rmdoc":
		return name, data, nil
	case ".epub":
		normalized, err := normalizeEPUB(data)
		if err != nil {
			return "", nil, fmt.Errorf("invalid EPUB: %w", err)
		}
		return name, normalized, nil
	case ".jpg", ".jpeg", ".png":
		pdf, err := imageToPDF(data)
		if err != nil {
			return "", nil, fmt.Errorf("could not convert image to PDF: %w", err)
		}
		return strings.TrimSuffix(name, filepath.Ext(name)) + ".pdf", pdf, nil
	default:
		return "", nil, fmt.Errorf("supported files: PDF, EPUB, RMDOC, JPG, JPEG, PNG")
	}
}

// normalizeEPUB rewrites the ZIP container so the required mimetype entry is
// first, uncompressed, and has no trailing data descriptor. reMarkable's USB
// importer otherwise identifies some valid-looking EPUB files as plain ZIPs.
func normalizeEPUB(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	if len(zr.File) > maxEPUBEntries {
		return nil, fmt.Errorf("EPUB has too many entries (maximum %d)", maxEPUBEntries)
	}
	var expanded uint64
	for _, file := range zr.File {
		if file.UncompressedSize64 > maxEPUBExpanded-expanded {
			return nil, fmt.Errorf("EPUB expanded content exceeds 128 MiB")
		}
		expanded += file.UncompressedSize64
	}
	var mimetypeFile *zip.File
	for _, file := range zr.File {
		if file.Name == "mimetype" {
			mimetypeFile = file
			break
		}
	}
	if mimetypeFile == nil {
		return nil, fmt.Errorf("missing mimetype entry")
	}
	r, err := mimetypeFile.Open()
	if err != nil {
		return nil, err
	}
	mimetype, err := io.ReadAll(io.LimitReader(r, 256))
	r.Close()
	if err != nil {
		return nil, err
	}
	if string(mimetype) != "application/epub+zip" {
		return nil, fmt.Errorf("incorrect mimetype entry")
	}

	out := limitedBuffer{limit: maxFileBytes}
	zw := zip.NewWriter(&out)
	h := &zip.FileHeader{
		Name:               "mimetype",
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE(mimetype),
		CompressedSize64:   uint64(len(mimetype)),
		UncompressedSize64: uint64(len(mimetype)),
	}
	h.SetModTime(time.Unix(0, 0))
	w, err := zw.CreateRaw(h)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(mimetype); err != nil {
		return nil, err
	}

	remaining := int64(maxEPUBExpanded)
	for _, file := range zr.File {
		if file.Name == "mimetype" {
			continue
		}
		r, err := file.Open()
		if err != nil {
			return nil, err
		}
		h := file.FileHeader
		h.Flags = 0
		h.CRC32 = 0
		h.CompressedSize = 0
		h.CompressedSize64 = 0
		h.UncompressedSize = 0
		h.UncompressedSize64 = 0
		w, err := zw.CreateHeader(&h)
		if err == nil {
			var n int64
			n, err = io.Copy(w, io.LimitReader(r, remaining+1))
			remaining -= n
			if remaining < 0 {
				err = fmt.Errorf("EPUB expanded content exceeds 128 MiB")
			}
		}
		r.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("converted file exceeds size limit")
	}
	return b.Buffer.Write(p)
}

func imageToPDF(data []byte) ([]byte, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxImagePixels/config.Height {
		return nil, fmt.Errorf("image exceeds 16 million pixels; resize it before sending")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 || w > maxImagePixels/h {
		return nil, fmt.Errorf("invalid or excessively large image")
	}

	// Flatten transparency onto white and encode the pixels as a PDF image stream.
	var pixels bytes.Buffer
	zw := zlib.NewWriter(&pixels)
	rgb := make([]byte, 0, 24*1024)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a < 0xffff {
				r += 0xffff - a
				g += 0xffff - a
				b += 0xffff - a
			}
			rgb = append(rgb, byte(r>>8), byte(g>>8), byte(b>>8))
			if len(rgb) == cap(rgb) {
				if _, err := zw.Write(rgb); err != nil {
					return nil, err
				}
				rgb = rgb[:0]
			}
		}
	}
	if _, err := zw.Write(rgb); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}

	const pageW, pageH, margin = 595.28, 841.89, 24.0 // A4 points
	scale := (pageW - 2*margin) / float64(w)
	if heightScale := (pageH - 2*margin) / float64(h); heightScale < scale {
		scale = heightScale
	}
	drawW, drawH := float64(w)*scale, float64(h)*scale
	x, y := (pageW-drawW)/2, (pageH-drawH)/2
	content := fmt.Sprintf("q\n%.3f 0 0 %.3f %.3f %.3f cm\n/Im0 Do\nQ\n", drawW, drawH, x, y)

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, 6)
	writeObject := func(number int, body []byte) {
		offsets[number] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", number)
		out.Write(body)
		out.WriteString("\nendobj\n")
	}
	writeObject(1, []byte("<< /Type /Catalog /Pages 2 0 R >>"))
	writeObject(2, []byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"))
	writeObject(3, []byte(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>", pageW, pageH)))
	writeObject(4, []byte(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content)))
	offsets[5] = out.Len()
	fmt.Fprintf(&out, "5 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", w, h, pixels.Len())
	out.Write(pixels.Bytes())
	out.WriteString("\nendstream\nendobj\n")
	xref := out.Len()
	out.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for n := 1; n <= 5; n++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[n])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return out.Bytes(), nil
}

func (s *server) importDocument(name string, data []byte) error {
	return s.importReader(name, bytes.NewReader(data), int64(len(data)))
}

func (s *server) importReader(name string, data io.Reader, size int64) error {
	if isUSBUploadURL(s.uploadURL) && !tcpReady("10.11.99.1:80", 300*time.Millisecond) {
		log.Printf("USB upload endpoint is unavailable; recovering it for cable-free use")
		if err := recoverUSBUploadEndpoint(); err != nil {
			return fmt.Errorf("could not start reMarkable importer without USB: %w", err)
		}
	}
	return s.postReader(name, data, size)
}

func (s *server) postDocument(name string, data []byte) error {
	return s.postReader(name, bytes.NewReader(data), int64(len(data)))
}

func (s *server) postReader(name string, data io.Reader, size int64) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	name = cleanFilename(name)
	contentType := "application/octet-stream"
	switch strings.ToLower(filepath.Ext(name)) {
	case ".epub":
		contentType = "application/epub+zip"
	case ".pdf":
		contentType = "application/pdf"
	case ".rmdoc":
		contentType = "application/octet-stream"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, strings.ReplaceAll(name, `"`, `'`)))
	header.Set("Content-Type", contentType)
	_, err := mw.CreatePart(header)
	if err != nil {
		return err
	}
	headerLen := body.Len()
	if err := mw.Close(); err != nil {
		return err
	}
	// Keep the file out of the multipart buffer, but retain Content-Length for
	// the native importer (which may not support chunked request bodies).
	envelope := body.Bytes()
	reader := io.MultiReader(bytes.NewReader(envelope[:headerLen]), data, bytes.NewReader(envelope[headerLen:]))
	req, err := http.NewRequest(http.MethodPost, s.uploadURL, reader)
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(envelope)) + size
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("upload API returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func isUSBUploadURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Scheme == "http" && u.Hostname() == "10.11.99.1"
}

func tcpReady(address string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func recoverUSBUploadEndpoint() error {
	interfaceName := ""
	for _, candidate := range []string{"usb1", "usb0"} {
		if _, err := net.InterfaceByName(candidate); err == nil {
			interfaceName = candidate
			break
		}
	}
	if interfaceName == "" {
		return fmt.Errorf("no USB network interface found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	ip, err := findIPCommand()
	if err != nil {
		return err
	}
	if err := runCommand(ctx, ip, "link", "set", "dev", interfaceName, "up"); err != nil {
		return err
	}
	addresses, err := exec.CommandContext(ctx, ip, "-4", "addr", "show", "dev", interfaceName).Output()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", interfaceName, err)
	}
	if !bytes.Contains(addresses, []byte("inet 10.11.99.1/")) {
		if err := runCommand(ctx, ip, "addr", "add", "10.11.99.1/32", "dev", interfaceName); err != nil {
			return err
		}
	}
	// xochitl watches USB network changes and starts its importer when the
	// interface regains 10.11.99.1. Do not restart xochitl here: recent Paper
	// Pro firmware treats that as a UI failure and may reboot the tablet.
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if tcpReady("10.11.99.1:80", 300*time.Millisecond) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("xochitl USB importer did not start after restoring %s", interfaceName)
}

func findIPCommand() (string, error) {
	for _, candidate := range []string{"/usr/sbin/ip", "/sbin/ip", "/bin/ip"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if candidate, err := exec.LookPath("ip"); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("ip command not found")
}

func runCommand(ctx context.Context, command string, args ...string) error {
	output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", command, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func makeEPUB(title, content, source string) ([]byte, error) {
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	// EPUB requires mimetype to be the first, uncompressed entry.
	mimetype := []byte("application/epub+zip")
	h := &zip.FileHeader{
		Name:               "mimetype",
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE(mimetype),
		CompressedSize64:   uint64(len(mimetype)),
		UncompressedSize64: uint64(len(mimetype)),
	}
	h.SetModTime(time.Unix(0, 0))
	w, err := zw.CreateRaw(h)
	if err != nil {
		return nil, err
	}
	if _, err = w.Write(mimetype); err != nil {
		return nil, err
	}
	files := map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
		"OEBPS/content.opf":      fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="bookid">urn:uuid:%d</dc:identifier><dc:title>%s</dc:title><dc:language>zh</dc:language><meta property="dcterms:modified">%s</meta></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/><item id="css" href="style.css" media-type="text/css"/></manifest><spine><itemref idref="chapter"/></spine></package>`, time.Now().UnixNano(), html.EscapeString(title), time.Now().UTC().Format("2006-01-02T15:04:05Z")),
		"OEBPS/nav.xhtml":        fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>%s</title></head><body><nav epub:type="toc" xmlns:epub="http://www.idpf.org/2007/ops"><ol><li><a href="chapter.xhtml">%s</a></li></ol></nav></body></html>`, html.EscapeString(title), html.EscapeString(title)),
		"OEBPS/chapter.xhtml":    fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>%s</title><link rel="stylesheet" type="text/css" href="style.css"/></head><body><article><h1 class="book-title">%s</h1>%s%s</article></body></html>`, html.EscapeString(title), html.EscapeString(title), markdownToXHTML(content), sourceBlock(source)),
		"OEBPS/style.css":        `body{font-family:"Noto Sans SC",sans-serif;font-weight:400;font-size:.88em;line-height:1.72;color:#111;margin:7% 6%;text-align:left;word-break:break-word}article{max-width:42em;margin:0 auto}.book-title{font-size:1.55em;font-weight:600;line-height:1.3;margin:0 0 1.35em;padding:0 0 .55em;border-bottom:2px solid #222}h2,h3,h4{font-weight:600;line-height:1.4;margin:1.35em 0 .65em}p{margin:0 0 .95em}ul,ol{margin:.25em 0 1em;padding-left:1.45em}li{margin:.35em 0}.message{margin:0 0 1.45em;padding:.95em 1em;page-break-inside:auto}.message.user{background:#ececea;border:1px solid #c8c8c4;border-radius:.55em}.message.assistant{border-left:3px solid #555;padding-left:1.1em}.role{font-size:.8em;font-weight:600;letter-spacing:.08em;color:#444;margin:0 0 .8em}.message-body>:first-child{margin-top:0}.message-body>:last-child{margin-bottom:0}pre{white-space:pre-wrap;overflow-wrap:anywhere;font-family:"Noto Sans Mono",monospace;font-size:.84em;line-height:1.5;background:#e8e8e5;border:1px solid #c7c7c2;border-radius:.4em;padding:.8em .9em;margin:.3em 0 1em}code{font-family:"Noto Sans Mono",monospace;font-size:.88em}blockquote{border-left:3px solid #777;padding:.15em 0 .15em .85em;margin:.2em 0 1em;color:#333}hr{border:0;border-top:1px solid #aaa;margin:1.35em 0}.source{font-size:.76em;line-height:1.45;color:#555;border-top:1px solid #aaa;margin-top:2.2em;padding-top:.7em;overflow-wrap:anywhere}a{color:#222;text-decoration:underline}`,
	}
	order := []string{"META-INF/container.xml", "OEBPS/content.opf", "OEBPS/nav.xhtml", "OEBPS/chapter.xhtml", "OEBPS/style.css"}
	for _, name := range order {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, files[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func markdownToXHTML(input string) string {
	body, inlineSource := splitTrailingSource(input)
	if rendered, ok := chatTranscriptToXHTML(body); ok {
		return rendered + sourceBlock(inlineSource)
	}
	return renderMarkdownBlocks(body) + sourceBlock(inlineSource)
}

func chatTranscriptToXHTML(input string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	type message struct {
		role string
		body []string
	}
	var messages []message
	var prefix []string
	var current *message
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "用户" || trimmed == "ChatGPT" {
			if current != nil {
				messages = append(messages, *current)
			}
			current = &message{role: trimmed}
			continue
		}
		if current == nil {
			prefix = append(prefix, line)
			continue
		}
		if trimmed == "---" {
			continue
		}
		current.body = append(current.body, line)
	}
	if current != nil {
		messages = append(messages, *current)
	}
	if len(messages) == 0 {
		return "", false
	}

	var out strings.Builder
	if strings.TrimSpace(strings.Join(prefix, "\n")) != "" {
		out.WriteString(`<div class="preface">`)
		out.WriteString(renderMarkdownBlocks(strings.Join(prefix, "\n")))
		out.WriteString(`</div>`)
	}
	for _, msg := range messages {
		className, label := "assistant", "ChatGPT"
		if msg.role == "用户" {
			className, label = "user", "你"
		}
		out.WriteString(`<section class="message ` + className + `"><div class="role">` + label + `</div><div class="message-body">`)
		out.WriteString(renderMarkdownBlocks(strings.TrimSpace(strings.Join(msg.body, "\n"))))
		out.WriteString(`</div></section>`)
	}
	return out.String(), true
}

func renderMarkdownBlocks(input string) string {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	lines := strings.Split(input, "\n")
	var out strings.Builder
	inCode, inList, orderedList := false, false, false
	closeList := func() {
		if !inList {
			return
		}
		if orderedList {
			out.WriteString("</ol>")
		} else {
			out.WriteString("</ul>")
		}
		inList, orderedList = false, false
	}
	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			closeList()
			if inCode {
				out.WriteString("</code></pre>")
				inCode = false
			} else {
				out.WriteString("<pre><code>")
				inCode = true
			}
			continue
		}
		if inCode {
			out.WriteString(html.EscapeString(line) + "\n")
			continue
		}
		trim := strings.TrimSpace(line)
		if trim == "" {
			closeList()
			continue
		}
		if strings.HasPrefix(trim, "- ") || strings.HasPrefix(trim, "* ") {
			if inList && orderedList {
				closeList()
			}
			if !inList {
				out.WriteString("<ul>")
				inList = true
			}
			out.WriteString("<li>" + inlineMarkdown(trim[2:]) + "</li>")
			continue
		}
		if item, ok := orderedListItem(trim); ok {
			if inList && !orderedList {
				closeList()
			}
			if !inList {
				out.WriteString("<ol>")
				inList, orderedList = true, true
			}
			out.WriteString("<li>" + inlineMarkdown(item) + "</li>")
			continue
		}
		closeList()
		n := 0
		for n < len(trim) && trim[n] == '#' {
			n++
		}
		if n > 0 && n <= 3 && len(trim) > n && trim[n] == ' ' {
			fmt.Fprintf(&out, "<h%d>%s</h%d>", n+1, inlineMarkdown(strings.TrimSpace(trim[n:])), n+1)
		} else if trim == "---" {
			out.WriteString("<hr/>")
		} else if strings.HasPrefix(trim, "> ") {
			out.WriteString("<blockquote><p>" + inlineMarkdown(trim[2:]) + "</p></blockquote>")
		} else {
			out.WriteString("<p>" + inlineMarkdown(trim) + "</p>")
		}
	}
	closeList()
	if inCode {
		out.WriteString("</code></pre>")
	}
	return out.String()
}

func orderedListItem(line string) (string, bool) {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) || (line[i] != '.' && line[i] != ')') || line[i+1] != ' ' {
		return "", false
	}
	return strings.TrimSpace(line[i+2:]), true
}

func splitTrailingSource(input string) (string, string) {
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	last := len(lines) - 1
	for last >= 0 && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	if last >= 0 {
		trimmed := strings.TrimSpace(lines[last])
		if strings.HasPrefix(trimmed, "来源：http://") || strings.HasPrefix(trimmed, "来源：https://") {
			source := strings.TrimSpace(strings.TrimPrefix(trimmed, "来源："))
			return strings.TrimSpace(strings.Join(lines[:last], "\n")), source
		}
	}
	return strings.TrimSpace(input), ""
}

func leadingMarkdownTitle(input string) (string, string) {
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# ")), strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
		break
	}
	return "", input
}

var linkRE = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^\s)]+)\)`)

func inlineMarkdown(s string) string {
	s = html.EscapeString(s)
	s = linkRE.ReplaceAllString(s, `<a href="$2">$1</a>`)
	return s
}

func sourceBlock(source string) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	escaped := html.EscapeString(strings.TrimSpace(source))
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return `<p class="source">来源：<a href="` + escaped + `">` + escaped + `</a></p>`
	}
	return `<p class="source">来源：` + escaped + `</p>`
}

func cleanTitle(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "))
	if len([]rune(s)) > 120 {
		s = string([]rune(s)[:120])
	}
	return s
}

func cleanFilename(s string) string {
	s = filepath.Base(strings.TrimSpace(s))
	for _, bad := range []string{"/", "\\", "\x00", "\n", "\r"} {
		s = strings.ReplaceAll(s, bad, "_")
	}
	if s == "" {
		s = "Web Inbox.epub"
	}
	return s
}

func respondJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}

const loginHTML = `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Web Inbox 配对</title><style>` + css + `</style><main><h1>Web Inbox</h1><p>请输入安装时显示的配对码。</p><form method="get"><label>配对码<input name="key" autocapitalize="characters" required></label><button>连接</button></form></main>`

const homeHTML = `<!doctype html><html lang="zh-CN"><meta name="viewport" content="width=device-width,initial-scale=1"><title>发送到 reMarkable</title><style>` + css + `</style><main><h1>发送到 reMarkable</h1><p class="hint">公开网页只需填写网址。ChatGPT 分享链接受 Cloudflare 限制，请在 Safari 打开后使用快捷指令，或粘贴正文/上传文件。</p><form id="textForm"><label>网页网址<input name="url" type="url" placeholder="https://example.com/article"></label><p class="or">— 或粘贴正文 —</p><label>标题（网址模式可留空）<input name="title" placeholder="文章标题"></label><label>正文<textarea name="content" rows="12" placeholder="粘贴 ChatGPT 回答、文本或 Markdown"></textarea></label><label>来源说明（可选）<input name="source" placeholder="网址模式会自动填写"></label><button>生成 EPUB 并发送</button></form><hr><form id="fileForm"><label>发送文件（PDF、EPUB、RMDOC、JPG、PNG）<input name="file" type="file" accept=".pdf,.epub,.rmdoc,.jpg,.jpeg,.png" required></label><p class="hint">JPG/JPEG 和 PNG 会自动转换为 PDF。</p><button>上传文件</button></form><p id="status"></p><details><summary>iPhone 快捷指令</summary><p>普通网页分享时向 <code>/api/send</code> POST <code>url</code>。ChatGPT 分享页需由 Safari 快捷指令读取页面中的 <code>title</code>、<code>content</code> 和 <code>source</code> 后提交。</p></details></main><script>
const status=document.querySelector('#status');
async function send(form,url){status.textContent='正在发送…';const b=form.querySelector('button');b.disabled=true;try{const r=await fetch(url,{method:'POST',body:new FormData(form)});const t=await r.text();if(!r.ok)throw new Error(t);status.textContent='✓ 已导入 reMarkable 文库';form.reset()}catch(e){status.textContent='发送失败：'+e.message}finally{b.disabled=false}}
document.querySelector('#textForm').onsubmit=e=>{e.preventDefault();send(e.target,'/api/send')};document.querySelector('#fileForm').onsubmit=e=>{e.preventDefault();send(e.target,'/api/upload')};
</script></html>`

const css = `*{box-sizing:border-box}body{margin:0;background:#f3f1eb;color:#181818;font:17px/1.45 -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif}main{max-width:720px;margin:auto;padding:28px 18px 60px}h1{font-size:30px}label{display:block;margin:18px 0 7px;font-weight:600}input,textarea{display:block;width:100%;margin-top:7px;padding:12px;border:1px solid #aaa;border-radius:8px;background:#fff;font:inherit}textarea{resize:vertical}button{width:100%;padding:13px;border:0;border-radius:9px;background:#181818;color:#fff;font:bold 17px inherit;margin-top:14px}button:disabled{opacity:.5}.hint{color:#555}.or{text-align:center;color:#777;margin:24px 0 4px}hr{border:0;border-top:1px solid #bbb;margin:32px 0}#status{font-weight:600}code{background:#ddd;padding:2px 4px;border-radius:3px}`
