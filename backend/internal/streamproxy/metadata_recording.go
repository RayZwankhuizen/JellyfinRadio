package streamproxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

type metadataRecording struct {
	file *os.File
	err  error
}

type metadataEvent struct {
	Type     string      `json:"type"`
	Time     time.Time   `json:"time"`
	Station  string      `json:"station"`
	Status   string      `json:"status,omitempty"`
	Protocol string      `json:"protocol,omitempty"`
	Headers  http.Header `json:"headers,omitempty"`
	RawICY   string      `json:"raw_icy,omitempty"`
}

func (p *Proxy) StartMetadataRecording(id, directory string) error {
	p.metadataMu.Lock()
	defer p.metadataMu.Unlock()
	if p.recordings[id] != nil {
		return nil
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, "radio-metadata-*.jsonl")
	if err != nil {
		return err
	}
	if p.recordings == nil {
		p.recordings = make(map[string]*metadataRecording)
	}
	recording := &metadataRecording{file: file}
	p.recordings[id] = recording
	metadata := p.metadata[id]
	p.recordMetadata(id, metadataEvent{Type: "start", Status: metadata.Status, Protocol: metadata.Protocol, Headers: metadata.Headers, RawICY: metadata.RawICY})
	if recording.err != nil {
		delete(p.recordings, id)
		_ = file.Close()
		_ = os.Remove(file.Name())
		return recording.err
	}
	delete(p.finishedRecordings, id)
	return nil
}

func (p *Proxy) StopMetadataRecording(id string) (string, error) {
	p.metadataMu.Lock()
	defer p.metadataMu.Unlock()
	recording := p.recordings[id]
	if recording == nil {
		return p.finishedRecordings[id], nil
	}
	p.recordMetadata(id, metadataEvent{Type: "stop"})
	delete(p.recordings, id)
	closeErr := recording.file.Close()
	if p.finishedRecordings == nil {
		p.finishedRecordings = make(map[string]string)
	}
	p.finishedRecordings[id] = recording.file.Name()
	if recording.err != nil || closeErr != nil {
		return recording.file.Name(), fmt.Errorf("incomplete metadata recording: write: %v, close: %v", recording.err, closeErr)
	}
	return recording.file.Name(), nil
}

// recordMetadata is called with metadataMu held to preserve event order.
func (p *Proxy) recordMetadata(id string, event metadataEvent) {
	recording := p.recordings[id]
	if recording == nil || recording.err != nil {
		return
	}
	event.Time, event.Station = time.Now().UTC(), id
	recording.err = json.NewEncoder(recording.file).Encode(event)
}
