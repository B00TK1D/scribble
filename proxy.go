package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProxyConfig holds the proxy configuration.
type ProxyConfig struct {
	ListenAddr string
	Upstream   *url.URL
	BaseFont   []byte
}

// Proxy returns an http.Handler that reverse-proxies to the upstream,
// intercepting HTML responses to inject font randomization.
func Proxy(cfg *ProxyConfig) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(cfg.Upstream)

	// Rewrite the request before sending upstream
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = cfg.Upstream.Host
	}

	// Intercept the response
	proxy.ModifyResponse = func(resp *http.Response) error {
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			return nil
		}

		// Read original body
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
		resp.Body.Close()

		seed := time.Now().UnixNano()
		key := GenerateFontKey()

		result, err := RandomizeFont(cfg.BaseFont, seed)
		if err != nil {
			log.Printf("font randomization failed: %v", err)
			// Pass through unmodified
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return nil
		}

		// Cache the font for serving
		fontCache.Store(key, result.FontBytes)

		// Rewrite HTML
		var out bytes.Buffer
		if err := RewriteHTML(bytes.NewReader(body), &out, key, result.CharMap); err != nil {
			log.Printf("html rewrite failed: %v", err)
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return nil
		}

		log.Printf("[%s] proxied %s (font key: %s, %d chars mapped)",
			resp.Request.URL.Path, time.Now().Format("15:04:05"), key, len(result.CharMap.Forward))

		resp.Body = io.NopCloser(&out)
		resp.Header.Del("Content-Length")
		resp.Header.Del("Content-Encoding") // we decoded it
		return nil
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle font requests directly
		if strings.HasPrefix(r.URL.Path, "/_scribble/font/") && strings.HasSuffix(r.URL.Path, ".ttf") {
			serveFont(w, r)
			return
		}

		proxy.ServeHTTP(w, r)
	})
}

// fontCache stores generated fonts keyed by their font key.
// Uses sync.Map for concurrent access without external locking.
var fontCache sync.Map

// serveFont handles requests for generated font files.
func serveFont(w http.ResponseWriter, r *http.Request) {
	// Extract key from path: /_scribble/font/{key}.ttf
	path := r.URL.Path
	key := strings.TrimPrefix(path, "/_scribble/font/")
	key = strings.TrimSuffix(key, ".ttf")

	val, ok := fontCache.Load(key)
	if !ok {
		http.Error(w, "font not found", http.StatusNotFound)
		return
	}

	fontBytes := val.([]byte)
	w.Header().Set("Content-Type", "font/ttf")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(fontBytes)
}
