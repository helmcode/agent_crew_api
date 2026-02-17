package runtime

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestTeamNamespaceName(t *testing.T) {
	tests := []struct {
		teamName string
		expected string
	}{
		{"myteam", "agentcrew-myteam"},
		{"prod-team", "agentcrew-prod-team"},
		{"dev", "agentcrew-dev"},
	}
	for _, tt := range tests {
		got := teamNamespaceName(tt.teamName)
		if got != tt.expected {
			t.Errorf("teamNamespaceName(%q) = %q, want %q", tt.teamName, got, tt.expected)
		}
	}
}

func TestAgentPodName(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"researcher", "agent-researcher"},
		{"worker-1", "agent-worker-1"},
		{"devops", "agent-devops"},
	}
	for _, tt := range tests {
		got := agentPodName(tt.name)
		if got != tt.expected {
			t.Errorf("agentPodName(%q) = %q, want %q", tt.name, got, tt.expected)
		}
	}
}

func TestStaticResourceNames(t *testing.T) {
	if got := workspacePVCName(); got != "workspace" {
		t.Errorf("workspacePVCName() = %q, want %q", got, "workspace")
	}
	if got := natsDeploymentName(); got != "nats" {
		t.Errorf("natsDeploymentName() = %q, want %q", got, "nats")
	}
	if got := natsServiceName(); got != "nats" {
		t.Errorf("natsServiceName() = %q, want %q", got, "nats")
	}
	if got := apiKeySecretName(); got != "anthropic-api-key" {
		t.Errorf("apiKeySecretName() = %q, want %q", got, "anthropic-api-key")
	}
}

func TestParseAgentID(t *testing.T) {
	tests := []struct {
		id        string
		wantNS    string
		wantPod   string
		wantError bool
	}{
		{"agentcrew-myteam/agent-researcher", "agentcrew-myteam", "agent-researcher", false},
		{"agentcrew-prod/agent-worker-1", "agentcrew-prod", "agent-worker-1", false},
		{"invalid-no-slash", "", "", true},
		{"", "", "", true},
		{"/missing-namespace", "", "", true},
		{"missing-pod/", "", "", true},
	}
	for _, tt := range tests {
		ns, pod, err := parseAgentID(tt.id)
		if tt.wantError {
			if err == nil {
				t.Errorf("parseAgentID(%q) expected error, got nil", tt.id)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseAgentID(%q) unexpected error: %v", tt.id, err)
			continue
		}
		if ns != tt.wantNS {
			t.Errorf("parseAgentID(%q) namespace = %q, want %q", tt.id, ns, tt.wantNS)
		}
		if pod != tt.wantPod {
			t.Errorf("parseAgentID(%q) podName = %q, want %q", tt.id, pod, tt.wantPod)
		}
	}
}

func TestPodPhaseToStatus(t *testing.T) {
	tests := []struct {
		phase    corev1.PodPhase
		expected string
	}{
		{corev1.PodRunning, "running"},
		{corev1.PodFailed, "error"},
		{corev1.PodSucceeded, "stopped"},
		{corev1.PodPending, "stopped"},
		{corev1.PodUnknown, "stopped"},
	}
	for _, tt := range tests {
		got := podPhaseToStatus(tt.phase)
		if got != tt.expected {
			t.Errorf("podPhaseToStatus(%q) = %q, want %q", tt.phase, got, tt.expected)
		}
	}
}

func TestGetNATSURL_K8s(t *testing.T) {
	k := &K8sRuntime{}
	tests := []struct {
		teamName string
		expected string
	}{
		{"myteam", "nats://nats.agentcrew-myteam.svc.cluster.local:4222"},
		{"prod", "nats://nats.agentcrew-prod.svc.cluster.local:4222"},
	}
	for _, tt := range tests {
		got := k.GetNATSURL(tt.teamName)
		if got != tt.expected {
			t.Errorf("K8sRuntime.GetNATSURL(%q) = %q, want %q", tt.teamName, got, tt.expected)
		}
	}
}

func TestGetNATSURL_Docker(t *testing.T) {
	d := &DockerRuntime{}
	tests := []struct {
		teamName string
		expected string
	}{
		{"myteam", "nats://team-myteam-nats:4222"},
		{"prod", "nats://team-prod-nats:4222"},
	}
	for _, tt := range tests {
		got := d.GetNATSURL(tt.teamName)
		if got != tt.expected {
			t.Errorf("DockerRuntime.GetNATSURL(%q) = %q, want %q", tt.teamName, got, tt.expected)
		}
	}
}
