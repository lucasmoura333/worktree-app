package main

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Anexos do leitor: GET /api/asset?doc=<caminho da nota>&path=<link do markdown>
// serve imagens, PDFs e arquivos referenciados pela nota. Só leitura, só dentro
// das mesmas raízes do leitor (docs canônico e docScope do Vault), com
// allowlist de extensão e limite de tamanho. O markdown nunca vira HTML: o
// leitor decide o que embutir a partir do kind devolvido em /api/doc.

const assetDefaultMaxMB = 50

var assetMimes = map[string]string{
	"png":  "image/png",
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"gif":  "image/gif",
	"webp": "image/webp",
	"svg":  "image/svg+xml",
	"pdf":  "application/pdf",
	"csv":  "text/csv; charset=utf-8",
	"txt":  "text/plain; charset=utf-8",
	"md":   "text/markdown; charset=utf-8",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"xls":  "application/vnd.ms-excel",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"doc":  "application/msword",
	"zip":  "application/zip",
	"7z":   "application/x-7z-compressed",
	"rar":  "application/vnd.rar",
}

var assetInline = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true, "svg": true,
	"pdf": true, "csv": true, "txt": true, "md": true,
}

var assetImage = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true, "svg": true,
}

// assetInfo vai no /api/doc para o leitor montar figura/card sem outro request.
type assetInfo struct {
	Size int64  `json:"size"`
	Mime string `json:"mime"`
	Kind string `json:"kind"` // image | pdf | file
	OK   bool   `json:"ok"`
}

var errAssetTarget = errors.New("anexo inválido")

func (s *server) assetRoots() []string {
	v := s.cfg.Vault
	if v == nil || v.Path == "" {
		return nil
	}
	roots := []string{filepath.Join(v.Path, filepath.FromSlash(strings.TrimSuffix(docScope, "/")))}
	if v.Docs != "" {
		roots = append(roots, v.Docs)
	}
	return roots
}

func (s *server) assetMaxBytes() int64 {
	mb := assetDefaultMaxMB
	if s.cfg.Reader != nil && s.cfg.Reader.MaxAssetMB > 0 {
		mb = s.cfg.Reader.MaxAssetMB
	}
	return int64(mb) << 20
}

// resolveAsset recebe a nota (mesmo path do /api/doc) e o link cru do markdown.
func (s *server) resolveAsset(docRel, link string) (string, assetInfo, error) {
	var info assetInfo
	if strings.TrimSpace(link) == "" {
		return "", info, errAssetTarget
	}
	_, cand, err := s.resolveDoc(docRel)
	if err != nil {
		return "", info, err
	}

	raw := strings.TrimSpace(link)
	raw = strings.TrimPrefix(raw, "<")
	raw = strings.TrimSuffix(raw, ">")
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" || strings.ContainsAny(raw, "\\\x00") || strings.Contains(raw, "://") || strings.HasPrefix(raw, "/") {
		return "", info, errAssetTarget
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil || strings.ContainsAny(decoded, ":\\\x00") {
		return "", info, errAssetTarget
	}

	target := filepath.Join(filepath.Dir(cand.file), filepath.FromSlash(decoded))
	real, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", info, os.ErrNotExist
	}

	allowed := false
	for _, root := range s.assetRoots() {
		if resolved, err := filepath.EvalSymlinks(root); err == nil && within(resolved, real) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", info, errDocScope
	}

	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() {
		return "", info, os.ErrNotExist
	}
	if st.Size() > s.assetMaxBytes() {
		return "", info, fmt.Errorf("anexo acima de %d MB", s.assetMaxBytes()>>20)
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(real), "."))
	mimeType, ok := assetMimes[ext]
	if !ok {
		return "", info, fmt.Errorf("extensão .%s não permitida", ext)
	}
	kind := "file"
	if assetImage[ext] {
		kind = "image"
	} else if ext == "pdf" {
		kind = "pdf"
	}
	return real, assetInfo{Size: st.Size(), Mime: mimeType, Kind: kind, OK: true}, nil
}

func (s *server) handleAsset(w http.ResponseWriter, r *http.Request) {
	abs, info, err := s.resolveAsset(r.URL.Query().Get("doc"), r.URL.Query().Get("path"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "anexo não encontrado", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		http.Error(w, "anexo não encontrado", http.StatusNotFound)
		return
	}
	defer f.Close()

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(abs), "."))
	disposition := "attachment"
	if assetInline[ext] {
		disposition = "inline"
	}
	h := w.Header()
	h.Set("Content-Type", info.Mime)
	if cd := mime.FormatMediaType(disposition, map[string]string{"filename": filepath.Base(abs)}); cd != "" {
		h.Set("Content-Disposition", cd)
	}
	h.Set("Cache-Control", "private, max-age=120")
	if ext == "svg" {
		// SVG pode executar script ao ser aberto direto; sem rede e sem inline.
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
	}
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "anexo não encontrado", http.StatusNotFound)
		return
	}
	http.ServeContent(w, r, filepath.Base(abs), st.ModTime().Truncate(time.Second), f)
}

var docLinkRe = regexp.MustCompile(`\]\(\s*<?([^)\s>]+)`)

// docAssets indexa os links locais da nota para o leitor saber tipo/tamanho.
func (s *server) docAssets(docRel, text string) map[string]assetInfo {
	out := map[string]assetInfo{}
	for _, m := range docLinkRe.FindAllStringSubmatch(text, 201) {
		link := m[1]
		if _, dup := out[link]; dup {
			continue
		}
		if strings.HasPrefix(link, "#") || strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
			continue
		}
		if _, info, err := s.resolveAsset(docRel, link); err == nil {
			out[link] = info
		} else if errors.Is(err, os.ErrNotExist) || errors.Is(err, errAssetTarget) || errors.Is(err, errDocScope) {
			out[link] = assetInfo{OK: false}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
