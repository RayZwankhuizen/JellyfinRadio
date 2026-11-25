package streamproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/raymo/jellyfin-smart-radio/backend/internal/store"
)

type StationSource interface {
	StreamURL(context.Context, string) (string, error)
}

type Proxy struct {
	source             StationSource
	client             *http.Client
	processorURL       string
	dspEnabled         bool
	dspSlots           chan struct{}
	metadataMu         sync.RWMutex
	metadata           map[string]StreamMetadata
	markerMu           sync.Mutex
	markerSuppression  map[string]bool
	markerVersions     map[string]uint64
	recordings         map[string]*metadataRecording
	finishedRecordings map[string]string
}

type StreamMetadata struct {
	Headers          http.Header   `json:"headers"`
	Status           string        `json:"status"`
	Protocol         string        `json:"protocol"`
	RawICY           string        `json:"raw_icy"`
	ICYHistory       []ICYMetadata `json:"icy_history,omitempty"`
	Title            string        `json:"title"`
	Artist           string        `json:"artist"`
	Track            string        `json:"track"`
	StreamURL        string        `json:"stream_url"`
	Received         bool          `json:"received"`
	UpdatedAt        time.Time     `json:"updated_at"`
	Recording        bool          `json:"recording"`
	Suppressed       bool          `json:"suppressed"`
	MarkerSuppressed bool          `json:"marker_suppressed"`
	connection       *byte
}

type ICYMetadata struct {
	Time        time.Time `json:"time"`
	RawICY      string    `json:"raw_icy"`
	StreamTitle string    `json:"streamtitle,omitempty"`
}

const maxICYHistory = 100

func New(source StationSource, allowPrivateHosts bool) *Proxy {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialPublic(ctx, network, address, allowPrivateHosts)
		},
		ResponseHeaderTimeout: 15 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		MaxConnsPerHost:       4,
		MaxIdleConnsPerHost:   2,
	}
	processorURL := strings.TrimRight(os.Getenv("AUDIO_SERVICE_URL"), "/")
	if processorURL == "" {
		processorURL = "http://smart-radio-audio:8790"
	}
	dspEnabled, _ := strconv.ParseBool(os.Getenv("AUDIO_DSP_ENABLED"))
	return &Proxy{
		source:            source,
		client:            &http.Client{Transport: transport, CheckRedirect: validateRedirect},
		processorURL:      processorURL,
		dspEnabled:        dspEnabled,
		dspSlots:          make(chan struct{}, 1),
		metadata:          make(map[string]StreamMetadata),
		markerSuppression: make(map[string]bool),
		markerVersions:    make(map[string]uint64),
	}
}

func (p *Proxy) Metadata(id string) StreamMetadata {
	p.metadataMu.RLock()
	metadata := p.metadata[id]
	metadata.ICYHistory = append([]ICYMetadata(nil), metadata.ICYHistory...)
	metadata.Recording = p.recordings[id] != nil
	p.metadataMu.RUnlock()
	p.markerMu.Lock()
	metadata.MarkerSuppressed = p.markerSuppression[id]
	p.markerMu.Unlock()
	return metadata
}

func (p *Proxy) SuppressionStatus(ctx context.Context, id string) bool {
	target, err := url.Parse(strings.TrimRight(p.processorURL, "/") + "/suppression")
	if err != nil {
		return false
	}
	query := target.Query()
	query.Set("station", id)
	target.RawQuery = query.Encode()
	ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false
	}
	response, err := (&http.Client{Timeout: 750 * time.Millisecond}).Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var state struct {
		Suppressed bool `json:"suppressed"`
	}
	return response.StatusCode == http.StatusOK && json.NewDecoder(response.Body).Decode(&state) == nil && state.Suppressed
}

func (p *Proxy) updateTitleSuppression(id, title string) {
	p.markerMu.Lock()
	active := p.markerSuppression[id]
	switch {
	case isArrowSuppressionMarker(title):
		active = true
	case isTrackTitle(title):
		active = false
	}
	changed := active != p.markerSuppression[id]
	p.markerMu.Unlock()
	if changed {
		p.setMarkerSuppression(id, active, false)
	}
}

func (p *Proxy) setMarkerSuppression(id string, active bool, force bool) {
	p.markerMu.Lock()
	if !force && p.markerSuppression[id] == active {
		p.markerMu.Unlock()
		return
	}
	p.markerSuppression[id] = active
	p.markerVersions[id]++
	version := p.markerVersions[id]
	p.markerMu.Unlock()

	body, _ := json.Marshal(struct {
		Station    string `json:"station"`
		Suppressed bool   `json:"suppressed"`
		Version    uint64 `json:"version"`
	}{id, active, version})
	request, err := http.NewRequest(http.MethodPost, p.processorURL+"/suppression/marker", bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	go func() {
		response, err := (&http.Client{Timeout: 750 * time.Millisecond}).Do(request)
		if err == nil {
			response.Body.Close()
		}
	}()
}

func isArrowSuppressionMarker(title string) bool {
	value := strings.ToUpper(strings.TrimLeft(strings.TrimSpace(title), " -\t"))
	return strings.HasPrefix(value, "ARROW - CLASSIC ROCK") || strings.HasPrefix(value, "HET ARROW CLASSIC ROCK NIEUWS")
}

func isTrackTitle(title string) bool {
	if isArrowSuppressionMarker(title) {
		return false
	}
	parts := strings.SplitN(title, " - ", 2)
	return len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != ""
}

func (p *Proxy) OpenLearningStream(ctx context.Context, id string, onMetadata func(string)) (io.ReadCloser, error) {
	target, err := p.source.StreamURL(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := validateStreamURL(target); err != nil {
		return nil, err
	}
	response, err := p.openAudio(ctx, target)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || isPlaylist(response.Header.Get("Content-Type")) {
		response.Body.Close()
		return nil, fmt.Errorf("station stream is unavailable or is an HLS playlist")
	}
	prepare := func(response *http.Response) {
		stripICYMetadata(response, func(packet []byte) {
			if title, ok := parseICYStreamTitle(packet); ok && onMetadata != nil {
				onMetadata(title)
			}
		})
	}
	prepare(response)
	return &reconnectingBody{proxy: p, ctx: ctx, target: target, body: response.Body, prepare: prepare}, nil
}

func (p *Proxy) clearMetadata(id string) {
	p.metadataMu.Lock()
	p.metadata[id] = StreamMetadata{UpdatedAt: time.Now().UTC()}
	p.metadataMu.Unlock()
	p.setMarkerSuppression(id, false, true)
}

func (p *Proxy) preparePlayback(id string, response *http.Response) {
	headers := response.Header.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	if len(response.TransferEncoding) > 0 {
		headers.Set("Transfer-Encoding", strings.Join(response.TransferEncoding, ", "))
	}
	connection := new(byte)
	p.metadataMu.Lock()
	history := p.metadata[id].ICYHistory
	p.metadata[id] = StreamMetadata{
		Headers: headers, Status: response.Status, Protocol: response.Proto, UpdatedAt: time.Now().UTC(), ICYHistory: history, connection: connection,
	}
	p.recordMetadata(id, metadataEvent{Type: "headers", Status: response.Status, Protocol: response.Proto, Headers: headers})
	p.metadataMu.Unlock()
	stripICYMetadata(response, func(packet []byte) {
		p.metadataMu.Lock()
		defer p.metadataMu.Unlock()
		metadata := p.metadata[id]
		if metadata.connection != connection {
			return
		}
		raw := strings.ToValidUTF8(strings.TrimRight(string(packet), "\x00"), "�")
		p.recordMetadata(id, metadataEvent{Type: "icy", RawICY: raw})
		if len(packet) == 0 {
			return
		}
		title, hasTitle := parseICYStreamTitle(packet)
		if hasTitle {
			p.updateTitleSuppression(id, title)
		}
		if raw != "" && (len(metadata.ICYHistory) == 0 || metadata.ICYHistory[len(metadata.ICYHistory)-1].RawICY != raw) {
			metadata.ICYHistory = append(metadata.ICYHistory, ICYMetadata{Time: time.Now().UTC(), RawICY: raw, StreamTitle: title})
			if len(metadata.ICYHistory) > maxICYHistory {
				metadata.ICYHistory = metadata.ICYHistory[len(metadata.ICYHistory)-maxICYHistory:]
			}
		}
		metadata.RawICY = raw
		metadata.UpdatedAt = time.Now().UTC()
		if hasTitle {
			metadata.Title, metadata.Artist, metadata.Track, metadata.Received = title, "", title, true
			if split := strings.Index(title, " - "); split > 0 {
				metadata.Artist = strings.TrimSpace(title[:split])
				metadata.Track = strings.TrimSpace(title[split+3:])
			}
		}
		p.metadata[id] = metadata
	})
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	const prefix = "/stream/"
	if !strings.HasPrefix(r.URL.Path, prefix) || strings.TrimPrefix(r.URL.Path, prefix) == "" || strings.Contains(strings.TrimPrefix(r.URL.Path, prefix), "/") {
		http.NotFound(w, r)
		return
	}
	id, err := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), prefix))
	if err != nil || id == "" {
		http.Error(w, "invalid station id", http.StatusBadRequest)
		return
	}
	streamURL, err := p.source.StreamURL(r.Context(), id)
	if err != nil {
		http.Error(w, "station not found", http.StatusNotFound)
		return
	}
	if err := validateStreamURL(streamURL); err != nil {
		http.Error(w, "invalid station stream URL", http.StatusBadGateway)
		return
	}
	p.clearMetadata(id)
	prepare := func(response *http.Response) { p.preparePlayback(id, response) }
	if p.dspEnabled && r.URL.Query().Get("playback") != "silero" {
		if source, ok := p.source.(interface {
			AudioSettings(context.Context, string) (store.AudioSettings, error)
			GlobalAudioSettings(context.Context) (store.GlobalAudioSettings, error)
			ApprovedStreamFingerprints(context.Context, string) ([]string, error)
		}); ok {
			settings, err := source.AudioSettings(r.Context(), id)
			if err == nil && settings.DSPEnabled {
				fingerprints, err := source.ApprovedStreamFingerprints(r.Context(), id)
				global, globalErr := source.GlobalAudioSettings(r.Context())
				if err == nil && globalErr == nil && global.NonMusicVolume < 1 {
					headers := http.Header{"X-Smart-Radio-Fingerprint": fingerprints, "X-Smart-Radio-Station": {id}}
					query := url.Values{
						"mode":      {"non-music"},
						"threshold": {strconv.FormatFloat(settings.ConfidenceThreshold, 'f', 4, 64)},
						"fade_down": {strconv.FormatFloat(settings.FadeOutSeconds, 'f', 4, 64)},
						"fade_up":   {strconv.FormatFloat(settings.FadeInSeconds, 'f', 4, 64)},
					}
					if p.tryProcessed(w, r, streamURL, global.NonMusicVolume, prepare, query, headers) {
						return
					}
				}
			}
		} else if settings, ok := p.source.(interface {
			StreamDSPSettings(context.Context, string) (bool, float64, error)
		}); ok {
			enabled, gain, settingsErr := settings.StreamDSPSettings(r.Context(), id)
			if settingsErr == nil && enabled && p.tryProcessed(w, r, streamURL, gain, prepare, nil, nil) {
				return
			}
		}
	}
	p.servePassthrough(w, r, streamURL, nil, prepare)
}

func (p *Proxy) servePassthrough(w http.ResponseWriter, r *http.Request, streamURL string, resp *http.Response, prepare func(*http.Response)) {
	var err error
	if resp == nil {
		resp, err = p.open(r.Context(), streamURL)
	}
	if err != nil {
		http.Error(w, "upstream stream unavailable", http.StatusBadGateway)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		http.Error(w, "upstream stream unavailable", http.StatusBadGateway)
		return
	}
	prepare(resp)
	copyStreamHeaders(w.Header(), resp.Header)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	buffer := make([]byte, 32*1024)
	attempt := 0
	for {
		_, copyErr := io.CopyBuffer(w, resp.Body, buffer)
		resp.Body.Close()
		if r.Context().Err() != nil {
			return
		}
		if copyErr != nil {
			attempt++
		}
		for r.Context().Err() == nil {
			if !wait(r.Context(), attempt) {
				return
			}
			candidate, openErr := p.open(r.Context(), streamURL)
			if openErr == nil && candidate.StatusCode >= 200 && candidate.StatusCode < 300 {
				prepare(candidate)
				resp = candidate
				attempt = 0
				break
			}
			if candidate != nil {
				candidate.Body.Close()
			}
			attempt++
		}
		if r.Context().Err() != nil {
			return
		}
	}
}

func (p *Proxy) tryProcessed(w http.ResponseWriter, r *http.Request, streamURL string, gain float64, prepare func(*http.Response), query url.Values, headers http.Header) bool {
	select {
	case p.dspSlots <- struct{}{}:
		defer func() { <-p.dspSlots }()
	default:
		return false
	}
	ctx, cancel := context.WithCancel(r.Context())
	upstream, err := p.openAudio(ctx, streamURL)
	if err != nil || upstream.StatusCode < 200 || upstream.StatusCode >= 300 || isPlaylist(upstream.Header.Get("Content-Type")) {
		if upstream != nil {
			upstream.Body.Close()
		}
		cancel()
		return false
	}
	prepare(upstream)

	pipeReader, pipeWriter := io.Pipe()
	body := &reconnectingBody{proxy: p, ctx: ctx, target: streamURL, body: upstream.Body, prepare: prepare}
	go func() {
		_, copyErr := io.Copy(pipeWriter, body)
		_ = pipeWriter.CloseWithError(copyErr)
	}()
	mode := "manual-gain"
	if query == nil {
		query = make(url.Values)
	} else {
		mode = query.Get("mode")
	}
	query.Set("gain", strconv.FormatFloat(gain, 'f', 4, 64))
	processURL := p.processorURL + "/process?" + query.Encode()
	response, connection, err := p.openProcessorStream(ctx, processURL, pipeReader, headers)
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		if response != nil {
			response.Body.Close()
		}
		if connection != nil {
			connection.Close()
		}
		cancel()
		body.Close()
		pipeReader.Close()
		pipeWriter.Close()
		return false
	}
	defer response.Body.Close()
	defer connection.Close()
	defer cancel()
	defer body.Close()
	defer pipeReader.Close()
	defer pipeWriter.Close()
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Smart-Radio-DSP", mode)
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	_, _ = io.Copy(w, response.Body)
	return true
}

func (p *Proxy) openProcessorStream(ctx context.Context, target string, body io.Reader, headers http.Header) (*http.Response, net.Conn, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil {
		return nil, nil, fmt.Errorf("audio processor must use an internal HTTP URL")
	}
	connection, err := (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, nil, err
	}
	stopClose := context.AfterFunc(ctx, func() { _ = connection.Close() })
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	requestHeaders := bufio.NewWriter(connection)
	_, _ = fmt.Fprintf(requestHeaders, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/octet-stream\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n", path, u.Host)
	_ = headers.Write(requestHeaders)
	_, _ = requestHeaders.WriteString("\r\n")
	if err := requestHeaders.Flush(); err != nil {
		stopClose()
		connection.Close()
		return nil, nil, err
	}
	go func() {
		writer := bufio.NewWriter(connection)
		buffer := make([]byte, 32*1024)
		for {
			n, readErr := body.Read(buffer)
			if n > 0 {
				if _, err := fmt.Fprintf(writer, "%x\r\n", n); err != nil {
					_ = connection.Close()
					return
				}
				if _, err := writer.Write(buffer[:n]); err != nil {
					_ = connection.Close()
					return
				}
				if _, err := writer.WriteString("\r\n"); err != nil || writer.Flush() != nil {
					_ = connection.Close()
					return
				}
			}
			if readErr != nil {
				_, _ = writer.WriteString("0\r\n\r\n")
				_ = writer.Flush()
				return
			}
		}
	}()
	_ = connection.SetReadDeadline(time.Now().Add(20 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
	if err != nil {
		stopClose()
		connection.Close()
		return nil, nil, err
	}
	_ = connection.SetReadDeadline(time.Time{})
	return response, connection, nil
}

func (p *Proxy) openAudio(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Icy-MetaData", "1")
	req.Header.Set("Accept", "audio/*,application/octet-stream,*/*")
	return p.client.Do(req)
}

func isPlaylist(contentType string) bool {
	value := strings.ToLower(contentType)
	return strings.Contains(value, "mpegurl") || strings.Contains(value, "x-mpegurl")
}

type reconnectingBody struct {
	proxy   *Proxy
	ctx     context.Context
	target  string
	body    io.ReadCloser
	prepare func(*http.Response)
}

func (b *reconnectingBody) Read(buffer []byte) (int, error) {
	for {
		if b.ctx.Err() != nil {
			return 0, io.EOF
		}
		n, _ := b.body.Read(buffer)
		if n > 0 {
			return n, nil
		}
		_ = b.body.Close()
		for attempt := 0; b.ctx.Err() == nil; attempt++ {
			if !wait(b.ctx, attempt) {
				return 0, io.EOF
			}
			candidate, openErr := b.proxy.openAudio(b.ctx, b.target)
			if openErr == nil && candidate.StatusCode >= 200 && candidate.StatusCode < 300 && !isPlaylist(candidate.Header.Get("Content-Type")) {
				b.prepare(candidate)
				b.body = candidate.Body
				break
			}
			if candidate != nil {
				candidate.Body.Close()
			}
		}
	}
}

func (b *reconnectingBody) Close() error {
	return b.body.Close()
}

func (p *Proxy) Check(ctx context.Context, target string) (int, time.Duration, error) {
	if err := validateStreamURL(target); err != nil {
		return 0, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	started := time.Now()
	resp, err := p.open(ctx, target)
	latency := time.Since(started)
	if err != nil {
		return 0, latency, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, latency, fmt.Errorf("stream returned %s", resp.Status)
	}
	buffer := make([]byte, 1)
	if n, err := io.ReadFull(resp.Body, buffer); err != nil || n == 0 {
		if err == nil {
			err = io.EOF
		}
		return resp.StatusCode, latency, fmt.Errorf("stream returned no audio: %w", err)
	}
	return resp.StatusCode, latency, nil
}

func (p *Proxy) open(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Icy-MetaData", "1")
	req.Header.Set("Accept", "*/*")
	return p.client.Do(req)
}

func validateStreamURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("unsupported stream URL")
	}
	if _, err := strconv.Atoi(u.Port()); u.Port() != "" && err != nil {
		return fmt.Errorf("invalid stream port")
	}
	return nil
}

func validateRedirect(req *http.Request, via []*http.Request) error {
	if err := validateStreamURL(req.URL.String()); err != nil {
		return err
	}
	if len(via) >= 10 {
		return fmt.Errorf("too many stream redirects")
	}
	return nil
}

func copyStreamHeaders(dst, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Content-Type") {
			for _, value := range values {
				dst.Add(key, value)
			}
		}
	}
}

func stripICYMetadata(resp *http.Response, onMetadata func([]byte)) {
	interval, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("icy-metaint")), 10, 64)
	if err == nil && interval > 0 {
		resp.Body = &icyMetadataReader{source: resp.Body, interval: interval, audioLeft: interval, onMetadata: onMetadata}
	}
}

type icyMetadataReader struct {
	source     io.Reader
	interval   int64
	audioLeft  int64
	onMetadata func([]byte)
}

func (r *icyMetadataReader) Close() error {
	if closer, ok := r.source.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (r *icyMetadataReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	for {
		if r.audioLeft == 0 {
			var size [1]byte
			if _, err := io.ReadFull(r.source, size[:]); err != nil {
				return 0, err
			}
			if metadataBytes := int64(size[0]) * 16; metadataBytes > 0 {
				metadata := make([]byte, metadataBytes)
				if _, err := io.ReadFull(r.source, metadata); err != nil {
					return 0, err
				}
				if r.onMetadata != nil {
					r.onMetadata(metadata)
				}
			} else if r.onMetadata != nil {
				r.onMetadata(nil)
			}
			r.audioLeft = r.interval
		}
		if int64(len(buffer)) > r.audioLeft {
			buffer = buffer[:r.audioLeft]
		}
		n, err := r.source.Read(buffer)
		r.audioLeft -= int64(n)
		if n > 0 || err != nil {
			return n, err
		}
	}
}

func parseICYStreamTitle(metadata []byte) (string, bool) {
	text := strings.ToValidUTF8(strings.TrimRight(string(metadata), "\x00"), "�")
	key := "streamtitle="
	start := strings.Index(strings.ToLower(text), key)
	if start < 0 {
		return "", false
	}
	value := strings.TrimSpace(text[start+len(key):])
	if value == "" {
		return "", true
	}
	if value[0] == '\'' || value[0] == '"' {
		quote := value[0]
		value = value[1:]
		if end := strings.IndexByte(value, quote); end >= 0 {
			value = value[:end]
		}
	} else if end := strings.IndexByte(value, ';'); end >= 0 {
		value = value[:end]
	}
	return strings.TrimSpace(value), true
}

func wait(ctx context.Context, attempt int) bool {
	delay := time.Second << min(attempt, 3)
	if delay > 8*time.Second {
		delay = 8 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

func dialPublic(ctx context.Context, network, address string, allowPrivate bool) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if allowPrivate {
		return dialer.DialContext(ctx, network, address)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		ips = []net.IP{net.IP(addr.AsSlice())}
	} else {
		resolved, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, host)
		if lookupErr != nil {
			return nil, lookupErr
		}
		for _, item := range resolved {
			ips = append(ips, item.IP)
		}
	}
	var lastErr error
	for _, ip := range ips {
		if !isPublicIP(ip) {
			continue
		}
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("stream host resolves only to non-public addresses")
}

func isPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	return !sharedAddressSpace.Contains(addr)
}
