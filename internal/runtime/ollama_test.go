package runtime

import (
	"testing"
)

func TestOllamaConstants(t *testing.T) {
	tests := []struct {
		name     string
		got      string
		notEmpty bool
	}{
		{"container name", OllamaContainerName, true},
		{"volume name", OllamaVolumeName, true},
		{"image", OllamaImage, true},
		{"internal port", OllamaInternalPort, true},
		{"internal url", OllamaInternalURL, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.notEmpty && tt.got == "" {
				t.Errorf("%s should not be empty", tt.name)
			}
		})
	}
}

func TestOllamaContainerName(t *testing.T) {
	if OllamaContainerName != "agentcrew-ollama" {
		t.Errorf("OllamaContainerName = %q, want %q", OllamaContainerName, "agentcrew-ollama")
	}
}

func TestOllamaVolumeName(t *testing.T) {
	if OllamaVolumeName != "agentcrew-ollama-models" {
		t.Errorf("OllamaVolumeName = %q, want %q", OllamaVolumeName, "agentcrew-ollama-models")
	}
}

func TestOllamaImage(t *testing.T) {
	if OllamaImage != "ollama/ollama:latest" {
		t.Errorf("OllamaImage = %q, want %q", OllamaImage, "ollama/ollama:latest")
	}
}

func TestOllamaInternalURL(t *testing.T) {
	if OllamaInternalURL != "http://agentcrew-ollama:11434" {
		t.Errorf("OllamaInternalURL = %q, want %q", OllamaInternalURL, "http://agentcrew-ollama:11434")
	}
}

func TestHasGPU(t *testing.T) {
	// hasGPU checks for nvidia-smi in PATH. The result depends on the host,
	// but the function should not panic.
	_ = hasGPU()
}

func TestOllamaInternalPortValue(t *testing.T) {
	if OllamaInternalPort != "11434" {
		t.Errorf("OllamaInternalPort = %q, want %q", OllamaInternalPort, "11434")
	}
}

func TestLabelInfra(t *testing.T) {
	if LabelInfra != "agentcrew.infra" {
		t.Errorf("LabelInfra = %q, want %q", LabelInfra, "agentcrew.infra")
	}
}
