package radiobrowser

import (
	"bytes"
	"fmt"
)

type bool01 bool

func (b *bool01) UnmarshalJSON(data []byte) error {
	switch string(bytes.TrimSpace(data)) {
	case "true", "1":
		*b = true
	case "false", "0", "null":
		*b = false
	default:
		return fmt.Errorf("invalid boolean value %s", data)
	}
	return nil
}

type Station struct {
	UUID          string `json:"stationuuid"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	ResolvedURL   string `json:"url_resolved"`
	Homepage      string `json:"homepage"`
	Favicon       string `json:"favicon"`
	Country       string `json:"country"`
	CountryCode   string `json:"countrycode"`
	Languages     string `json:"language"`
	Tags          string `json:"tags"`
	Codec         string `json:"codec"`
	Bitrate       int    `json:"bitrate"`
	LastCheckOK   bool01 `json:"lastcheckok"`
	LastCheckTime string `json:"lastchecktime_iso8601"`
}

func (s Station) StreamURL() string {
	if s.ResolvedURL != "" {
		return s.ResolvedURL
	}
	return s.URL
}
