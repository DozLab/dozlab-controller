package controller

import (
	"strings"
	"testing"
)

func TestPodSettingsValidate(t *testing.T) {
	valid := PodSettings{
		VMImage:       "vm:1",
		InitImage:     "init:1",
		TerminalImage: "term:1",
		SSHUser:       "root",
		VMDiskSize:    "4Gi",
	}

	testCases := []struct {
		name    string
		mutate  func(*PodSettings)
		wantErr string
	}{
		{"valid", func(s *PodSettings) {}, ""},
		{"missing VMImage", func(s *PodSettings) { s.VMImage = "" }, "vm image is required"},
		{"missing InitImage", func(s *PodSettings) { s.InitImage = "" }, "init image is required"},
		{"missing TerminalImage", func(s *PodSettings) { s.TerminalImage = "" }, "terminal image is required"},
		{"empty SSHUser", func(s *PodSettings) { s.SSHUser = "" }, "ssh user is required"},
		{"unparsable VMDiskSize", func(s *PodSettings) { s.VMDiskSize = "lots" }, "invalid vm disk size"},
		{"tiny VMDiskSize", func(s *PodSettings) { s.VMDiskSize = "100Ki" }, "below 1Mi"},
		{"https PublicBaseURL", func(s *PodSettings) { s.PublicBaseURL = "https://lab.example.ts.net" }, ""},
		{"PublicBaseURL without scheme", func(s *PodSettings) { s.PublicBaseURL = "lab.example.ts.net" }, "must be an http(s) URL"},
		{"PublicBaseURL with other scheme", func(s *PodSettings) { s.PublicBaseURL = "ftp://lab.example" }, "must be an http(s) URL"},
		{"all three images missing", func(s *PodSettings) {
			s.VMImage = ""
			s.InitImage = ""
			s.TerminalImage = ""
		}, "init image is required"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			podSettings := valid
			tc.mutate(&podSettings)
			err := podSettings.Validate()
			if tc.wantErr != "" {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tc.wantErr)
				} else if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("expected error containing %q, got %q", tc.wantErr, err)
				}
			} else if err != nil {
				t.Errorf("expected no error, got %q", err)
			}
		})
	}
}
