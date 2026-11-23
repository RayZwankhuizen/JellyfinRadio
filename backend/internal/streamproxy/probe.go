package streamproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ProbeResult struct {
	StatusCode   int      `json:"status_code"`
	ContentType  string   `json:"content_type"`
	Format       string   `json:"format"`
	SampleBytes  int      `json:"sample_bytes"`
	DSPRequested bool     `json:"dsp_requested"`
	DSPApplied   bool     `json:"dsp_applied"`
	Gain         *float64 `json:"gain,omitempty"`
	LatencyMS    int64    `json:"latency_ms"`
	Error        string   `json:"error,omitempty"`
}

type probeWriter struct {
	header http.Header
	status int
	sample []byte
	cancel context.CancelFunc
}

const probeSampleBytes = 8 * 1024
const minimumPlayableSampleBytes = 1024

func (w *probeWriter) Header() http.Header { return w.header }
func (w *probeWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *probeWriter) Flush() {}
func (w *probeWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n := len(data)
	if len(w.sample) < probeSampleBytes {
		remaining := probeSampleBytes - len(w.sample)
		if len(data) > remaining {
			data = data[:remaining]
		}
		w.sample = append(w.sample, data...)
	}
	if len(w.sample) >= probeSampleBytes {
		w.cancel()
	}
	return n, nil
}

type dspProbeSource struct{ StationSource }

func (s dspProbeSource) StreamDSPSettings(ctx context.Context, id string) (bool, float64, error) {
	settings, ok := s.StationSource.(interface {
		StreamDSPSettings(context.Context, string) (bool, float64, error)
	})
	if !ok {
		return false, 0, fmt.Errorf("station DSP settings unavailable")
	}
	_, gain, err := settings.StreamDSPSettings(ctx, id)
	return true, gain, err
}

// Probe exercises the same stream handler as playback and captures up to 8 KiB.
// DSP probing requires global opt-in but does not change station settings.
func (p *Proxy) Probe(ctx context.Context, id string, dsp bool) (ProbeResult, error) {
	result := ProbeResult{DSPRequested: dsp}
	if dsp && !p.dspEnabled {
		return result, fmt.Errorf("DSP is disabled globally")
	}
	probe := Proxy{
		source:       p.source,
		client:       p.client,
		processorURL: p.processorURL,
		dspSlots:     p.dspSlots,
		metadata:     make(map[string]StreamMetadata),
	}
	if dsp {
		probe.source = dspProbeSource{p.source}
		probe.dspEnabled = true
		if settings, ok := p.source.(interface {
			StreamDSPSettings(context.Context, string) (bool, float64, error)
		}); ok {
			_, gain, settingsErr := settings.StreamDSPSettings(ctx, id)
			if settingsErr == nil {
				result.Gain = &gain
			}
		}
	} else {
		probe.dspEnabled = false
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	streamURL := (&url.URL{Scheme: "http", Host: "smart-radio", Path: "/stream/" + id}).String()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return result, err
	}
	writer := &probeWriter{header: make(http.Header), cancel: cancel}
	started := time.Now()
	probe.ServeHTTP(writer, request)
	result.StatusCode = writer.status
	result.ContentType = writer.header.Get("Content-Type")
	result.Format = identifyFormat(result.ContentType, writer.sample)
	result.SampleBytes = len(writer.sample)
	result.DSPApplied = writer.header.Get("X-Smart-Radio-DSP") == "manual-gain"
	result.LatencyMS = time.Since(started).Milliseconds()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		result.Error = fmt.Sprintf("stream proxy returned HTTP %d", result.StatusCode)
	} else if result.SampleBytes < minimumPlayableSampleBytes {
		result.Error = fmt.Sprintf("only %d audio bytes received; not enough to verify playable output", result.SampleBytes)
	}
	return result, nil
}

func identifyFormat(contentType string, sample []byte) string {
	typeName := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch {
	case strings.Contains(typeName, "mpegurl") || strings.HasPrefix(strings.TrimSpace(string(sample)), "#EXTM3U"):
		return "HLS-playlist"
	case strings.Contains(typeName, "audio/mpeg") || strings.HasPrefix(string(sample), "ID3"):
		return "MP3/MPEG audio"
	case strings.Contains(typeName, "audio/aac") || len(sample) > 1 && sample[0] == 0xff && sample[1]&0xf6 == 0xf0:
		return "AAC audio"
	case strings.Contains(typeName, "audio/ogg") || len(sample) >= 4 && string(sample[:4]) == "OggS":
		return "Ogg audio"
	case strings.Contains(typeName, "audio/flac") || len(sample) >= 4 && string(sample[:4]) == "fLaC":
		return "FLAC audio"
	case strings.Contains(typeName, "audio/wav") || len(sample) >= 12 && string(sample[:4]) == "RIFF" && string(sample[8:12]) == "WAVE":
		return "WAV audio"
	case typeName != "":
		return typeName
	default:
		return "unknown format"
	}
}
