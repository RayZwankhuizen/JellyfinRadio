package streamproxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type sourceFunc func(context.Context, string) (string, error)

func (f sourceFunc) StreamURL(ctx context.Context, id string) (string, error) { return f(ctx, id) }

type dspSource struct{ url string }

func (s dspSource) StreamURL(context.Context, string) (string, error) { return s.url, nil }
func (dspSource) StreamDSPSettings(context.Context, string) (bool, float64, error) {
	return true, 0.25, nil
}

type disabledDSPSource struct{ url string }

func (s disabledDSPSource) StreamURL(context.Context, string) (string, error) { return s.url, nil }
func (disabledDSPSource) StreamDSPSettings(context.Context, string) (bool, float64, error) {
	return false, 0.4, nil
}

func TestProxyFollowsRedirectAndStripsICYMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/live", http.StatusFound)
			return
		}
		if r.Header.Get("Icy-MetaData") != "1" {
			t.Errorf("proxy should request ICY metadata for track information")
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("icy-name", "Test Radio")
		w.Header().Set("icy-metaint", "16000")
		_, _ = io.WriteString(w, "audio-bytes")
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(New(sourceFunc(func(context.Context, string) (string, error) {
		return upstream.URL + "/redirect", nil
	}), true))
	defer proxy.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/stream/station-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "audio/mpeg" || resp.Header.Get("Icy-Metaint") != "" {
		t.Fatalf("unexpected proxy response: %d %v", resp.StatusCode, resp.Header)
	}
	body := make([]byte, len("audio-bytes"))
	if _, err := io.ReadFull(resp.Body, body); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
	if string(body) != "audio-bytes" {
		t.Fatalf("body = %q", body)
	}
}

func TestPlaybackCapturesHeadersAndFullICYPacket(t *testing.T) {
	proxy := New(nil, true)
	if err := proxy.StartMetadataRecording("station", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	packet := "StreamTitle='Artist - Track';StreamUrl='https://radio.example';"
	blocks := (len(packet) + 15) / 16
	audio := "abcd" + string(byte(blocks)) + packet + strings.Repeat("\x00", blocks*16-len(packet)) + "efgh"
	response := &http.Response{
		Status: "200 OK",
		Proto:  "HTTP/1.1",
		Header: http.Header{
			"Icy-Metaint": {"4"},
			"X-Test":      {"first", "second"},
		},
		TransferEncoding: []string{"chunked"},
		Body:             io.NopCloser(strings.NewReader(audio)),
	}
	proxy.preparePlayback("station", response)
	got := make([]byte, 8)
	if _, err := io.ReadFull(response.Body, got); err != nil || string(got) != "abcdefgh" {
		t.Fatalf("audio: %q, error: %v", got, err)
	}
	metadata := proxy.Metadata("station")
	if metadata.RawICY != packet || metadata.Title != "Artist - Track" || metadata.Track != "Track" {
		t.Fatalf("metadata: %+v", metadata)
	}
	if metadata.Status != "200 OK" || len(metadata.Headers.Values("X-Test")) != 2 || metadata.Headers.Get("Transfer-Encoding") != "chunked" {
		t.Fatalf("headers: %v, status: %q", metadata.Headers, metadata.Status)
	}
	path, err := proxy.StopMetadataRecording("station")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != 4 || !strings.Contains(lines[1], `"X-Test":["first","second"]`) || !strings.Contains(lines[2], packet) || !strings.Contains(lines[3], `"type":"stop"`) {
		t.Fatalf("recording: %s", contents)
	}
}

func TestProcessedStreamUsesAudioServiceAndReturnsMP3(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Icy-MetaData") != "1" {
			t.Error("processed request should ask for track metadata")
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, "radio-input")
	}))
	defer upstream.Close()

	processor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/process" || r.URL.Query().Get("gain") != "0.2500" {
			t.Errorf("processor request: %s %s", r.Method, r.URL.String())
		}
		if len(r.TransferEncoding) != 1 || r.TransferEncoding[0] != "chunked" {
			t.Errorf("audio input must stream as chunked body; encoding=%v", r.TransferEncoding)
		}
		if _, err := io.CopyN(io.Discard, r.Body, 1); err != nil {
			t.Errorf("processor could not read stream body: %v", err)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, strings.Repeat("processed-mp3", 1024))
	}))
	defer processor.Close()

	proxy := New(dspSource{url: upstream.URL}, true)
	proxy.dspEnabled = true
	proxy.processorURL = processor.URL
	probe, err := proxy.Probe(context.Background(), "test", true)
	if err != nil || probe.StatusCode != http.StatusOK || !probe.DSPApplied || probe.ContentType != "audio/mpeg" || probe.SampleBytes != probeSampleBytes || probe.Error != "" || probe.Gain == nil || *probe.Gain != 0.25 {
		t.Fatalf("DSP probe: %+v err=%v", probe, err)
	}
	server := httptest.NewServer(proxy)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/stream/test", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Smart-Radio-DSP") != "manual-gain" || !strings.HasPrefix(string(body), "processed-mp3") {
		t.Fatalf("processed response status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
	}
}

func TestProbeWithoutDSPReadsRawProxyOutput(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/aac")
		_, _ = io.WriteString(w, strings.Repeat("raw-audio-sample", 1024))
	}))
	defer upstream.Close()
	probe, err := New(sourceFunc(func(context.Context, string) (string, error) { return upstream.URL, nil }), true).Probe(context.Background(), "test", false)
	if err != nil || probe.StatusCode != http.StatusOK || probe.DSPApplied || probe.ContentType != "audio/aac" || probe.Format != "AAC audio" || probe.SampleBytes != probeSampleBytes || probe.Error != "" {
		t.Fatalf("raw probe: %+v err=%v", probe, err)
	}
}

func TestProbeDoesNotTreatTinyEncoderHeaderAsPlayableAudio(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("source-audio", 8))
	}))
	defer upstream.Close()
	processor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.CopyN(io.Discard, r.Body, 1)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, strings.Repeat("x", 45))
	}))
	defer processor.Close()
	proxy := New(dspSource{url: upstream.URL}, true)
	proxy.dspEnabled = true
	proxy.processorURL = processor.URL
	probe, err := proxy.Probe(context.Background(), "test", true)
	if err != nil || probe.SampleBytes != 45 || probe.Error == "" {
		t.Fatalf("tiny DSP output was treated as playable: %+v err=%v", probe, err)
	}
}

func TestDSPProbeUsesCurrentStationGainEvenWhenStationToggleIsOff(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "raw-audio") }))
	defer upstream.Close()
	processor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("gain") != "0.4000" {
			t.Errorf("probe gain = %q", r.URL.Query().Get("gain"))
		}
		_, _ = io.CopyN(io.Discard, r.Body, 1)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, strings.Repeat("encoded-audio", 1024))
	}))
	defer processor.Close()
	proxy := New(disabledDSPSource{upstream.URL}, true)
	proxy.dspEnabled = true
	proxy.processorURL = processor.URL
	probe, err := proxy.Probe(context.Background(), "station", true)
	if err != nil || !probe.DSPApplied || probe.Gain == nil || *probe.Gain != 0.4 || probe.SampleBytes != probeSampleBytes || probe.Error != "" {
		t.Fatalf("DSP probe=%+v err=%v", probe, err)
	}
}

func TestProcessedStreamFallsBackBeforeOutputStarts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, "original-radio-audio")
	}))
	defer upstream.Close()
	processor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "processor unavailable", http.StatusServiceUnavailable)
	}))
	defer processor.Close()

	proxy := New(dspSource{url: upstream.URL}, true)
	proxy.dspEnabled = true
	proxy.processorURL = processor.URL
	server := httptest.NewServer(proxy)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/stream/test", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	buffer := make([]byte, len("original-radio-audio"))
	if _, err := io.ReadFull(response.Body, buffer); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Smart-Radio-DSP") != "" || string(buffer) != "original-radio-audio" {
		t.Fatalf("fallback response status=%d headers=%v body=%q", response.StatusCode, response.Header, buffer)
	}
}

func TestGlobalDSPOffKeepsEnabledStationOnPassthrough(t *testing.T) {
	t.Setenv("AUDIO_DSP_ENABLED", "false")
	var processorCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, "original-radio-audio")
	}))
	defer upstream.Close()
	processor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		processorCalls.Add(1)
		_, _ = io.WriteString(w, "unexpected-dsp")
	}))
	defer processor.Close()
	t.Setenv("AUDIO_SERVICE_URL", processor.URL)

	server := httptest.NewServer(New(dspSource{url: upstream.URL}, true))
	defer server.Close()
	response, err := http.Get(server.URL + "/stream/test")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	buffer := make([]byte, len("original-radio-audio"))
	if _, err := io.ReadFull(response.Body, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "original-radio-audio" || response.Header.Get("X-Smart-Radio-DSP") != "" || processorCalls.Load() != 0 {
		t.Fatalf("DSP default was not passthrough: data=%q headers=%v calls=%d", buffer, response.Header, processorCalls.Load())
	}
}

func TestProcessedStreamWithFFmpegService(t *testing.T) {
	processorURL := os.Getenv("JFR_AUDIO_PROCESSOR_URL")
	if processorURL == "" {
		t.Skip("set JFR_AUDIO_PROCESSOR_URL to run the real FFmpeg integration test")
	}
	input := testToneWAV()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Length", fmt.Sprint(len(input)))
		_, _ = w.Write(input)
	}))
	defer upstream.Close()

	proxy := New(dspSource{url: upstream.URL}, true)
	proxy.dspEnabled = true
	proxy.processorURL = strings.TrimRight(processorURL, "/")
	server := httptest.NewServer(proxy)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/stream/test", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	output := make([]byte, 1024)
	if _, err := io.ReadFull(response.Body, output); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("FFmpeg stream response status=%d type=%s", response.StatusCode, response.Header.Get("Content-Type"))
	}
}

func testToneWAV() []byte {
	const sampleRate = 16000
	const samples = sampleRate
	data := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		value := int16(10000 * math.Sin(2*math.Pi*440*float64(i)/sampleRate))
		binary.LittleEndian.PutUint16(data[i*2:], uint16(value))
	}
	output := make([]byte, 44+len(data))
	copy(output[0:4], "RIFF")
	binary.LittleEndian.PutUint32(output[4:8], uint32(36+len(data)))
	copy(output[8:16], "WAVEfmt ")
	binary.LittleEndian.PutUint32(output[16:20], 16)
	binary.LittleEndian.PutUint16(output[20:22], 1)
	binary.LittleEndian.PutUint16(output[22:24], 1)
	binary.LittleEndian.PutUint32(output[24:28], sampleRate)
	binary.LittleEndian.PutUint32(output[28:32], sampleRate*2)
	binary.LittleEndian.PutUint16(output[32:34], 2)
	binary.LittleEndian.PutUint16(output[34:36], 16)
	copy(output[36:40], "data")
	binary.LittleEndian.PutUint32(output[40:44], uint32(len(data)))
	copy(output[44:], data)
	return output
}

func TestProxyReconnectsAfterUpstreamEnds(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "ab")
	}))
	defer upstream.Close()
	proxy := httptest.NewServer(New(sourceFunc(func(context.Context, string) (string, error) {
		return upstream.URL, nil
	}), true))
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/stream/station", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4)
	if _, err := io.ReadFull(resp.Body, body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if string(body) != "abab" || requests.Load() < 2 {
		t.Fatalf("body=%q requests=%d", body, requests.Load())
	}
}

func TestCheckValidatesAudioBodyAndLatency(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "audio") }))
	defer upstream.Close()
	status, latency, err := New(sourceFunc(nil), true).Check(context.Background(), upstream.URL)
	if err != nil || status != http.StatusOK || latency < 0 {
		t.Fatalf("check status=%d latency=%s error=%v", status, latency, err)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer empty.Close()
	if _, _, err := New(sourceFunc(nil), true).Check(context.Background(), empty.URL); err == nil {
		t.Fatal("empty stream passed health check")
	}
}

func TestProxyRejectsInvalidTargetsAndMethods(t *testing.T) {
	proxy := New(sourceFunc(func(context.Context, string) (string, error) { return "file:///etc/passwd", nil }), true)
	for _, target := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/stream/station", http.StatusBadGateway},
		{http.MethodGet, "/stream/", http.StatusNotFound},
		{http.MethodPost, "/stream/station", http.StatusMethodNotAllowed},
	} {
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, httptest.NewRequest(target.method, target.path, nil))
		if rec.Code != target.want {
			t.Errorf("%s %s: status=%d, want %d", target.method, target.path, rec.Code, target.want)
		}
	}
	if err := validateStreamURL("http://user:pass@example.com/stream"); err == nil {
		t.Fatal("userinfo should be rejected")
	}
}

func TestPrivateAddressesAreBlocked(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fd00::1"} {
		if isPublicIP(net.ParseIP(ip)) {
			t.Errorf("%s should be blocked", ip)
		}
	}
}
