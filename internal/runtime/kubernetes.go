package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// K8sRuntime implements AgentRuntime using the Kubernetes API.
type K8sRuntime struct {
	clientset kubernetes.Interface
}

// NewK8sRuntime creates a K8sRuntime, trying in-cluster config first,
// then falling back to kubeconfig.
func NewK8sRuntime() (*K8sRuntime, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig.
		kubeconfigPath := os.Getenv("KUBECONFIG")
		if kubeconfigPath == "" {
			home, _ := os.UserHomeDir()
			kubeconfigPath = filepath.Join(home, ".kube", "config")
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("creating k8s config: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating k8s clientset: %w", err)
	}

	return &K8sRuntime{clientset: clientset}, nil
}

// Naming conventions for Kubernetes resources.
func teamNamespaceName(teamName string) string { return "agentcrew-" + teamName }
func agentPodName(name string) string          { return "agent-" + name }
func workspacePVCName() string                 { return "workspace" }
func natsDeploymentName() string               { return "nats" }
func natsServiceName() string                  { return "nats" }
func apiKeySecretName() string                 { return "anthropic-api-key" }

// parseAgentID splits a compound agent ID ("namespace/podName") into its parts.
func parseAgentID(id string) (namespace, podName string, err error) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid agent ID %q: expected namespace/name", id)
	}
	return parts[0], parts[1], nil
}

// GetNATSURL returns the NATS URL for a team in Kubernetes runtime using in-cluster DNS.
func (k *K8sRuntime) GetNATSURL(teamName string) string {
	return "nats://nats.agentcrew-" + teamName + ".svc.cluster.local:4222"
}

// DeployInfra creates the namespace, workspace PVC, and optionally NATS deployment+service.
func (k *K8sRuntime) DeployInfra(ctx context.Context, config InfraConfig) error {
	if err := validateName(config.TeamName); err != nil {
		return fmt.Errorf("invalid team name: %w", err)
	}
	ns := teamNamespaceName(config.TeamName)
	slog.Info("deploying k8s team infrastructure", "team", config.TeamName, "namespace", ns)

	// Create namespace.
	_, err := k.clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: map[string]string{LabelTeam: config.TeamName},
		},
	}, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %s: %w", ns, err)
	}

	// Create workspace PVC.
	_, err = k.clientset.CoreV1().PersistentVolumeClaims(ns).Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   workspacePVCName(),
			Labels: map[string]string{LabelTeam: config.TeamName},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating PVC: %w", err)
	}

	// Deploy NATS if enabled.
	if config.NATSEnabled {
		if err := k.deployNATS(ctx, config.TeamName, ns); err != nil {
			return fmt.Errorf("deploying nats: %w", err)
		}
	}

	slog.Info("k8s team infrastructure deployed", "team", config.TeamName)
	return nil
}

// natsAuthSecretName returns the name of the NATS auth token secret.
func natsAuthSecretName() string { return "nats-auth-token" }

// ensureNATSAuthSecret creates a Kubernetes Secret for the NATS auth token
// if one is configured and it doesn't already exist.
func (k *K8sRuntime) ensureNATSAuthSecret(ctx context.Context, namespace string) (bool, error) {
	token := os.Getenv("NATS_AUTH_TOKEN")
	if token == "" {
		slog.Warn("NATS_AUTH_TOKEN not set, NATS running without authentication")
		return false, nil
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: natsAuthSecretName(),
		},
		StringData: map[string]string{
			"token": token,
		},
	}

	_, err := k.clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return false, fmt.Errorf("creating nats auth secret: %w", err)
	}
	return true, nil
}

// deployNATS creates a NATS Deployment and ClusterIP Service, then waits for readiness.
// The auth token is stored in a Kubernetes Secret and injected via env var to avoid
// exposing it in the Deployment spec args.
func (k *K8sRuntime) deployNATS(ctx context.Context, teamName, namespace string) error {
	hasAuth, err := k.ensureNATSAuthSecret(ctx, namespace)
	if err != nil {
		return fmt.Errorf("ensuring nats auth secret: %w", err)
	}

	// Build container spec. When auth is configured, use a shell command to
	// read the token from the env var so it never appears in the pod spec args.
	var natsContainer corev1.Container
	if hasAuth {
		natsContainer = corev1.Container{
			Name:    "nats",
			Image:   NATSImage,
			Command: []string{"sh", "-c", "exec nats-server --jetstream --auth \"$NATS_AUTH_TOKEN\""},
			Env: []corev1.EnvVar{
				{
					Name: "NATS_AUTH_TOKEN",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: natsAuthSecretName(),
							},
							Key: "token",
						},
					},
				},
			},
			Ports: []corev1.ContainerPort{{ContainerPort: 4222, Protocol: corev1.ProtocolTCP}},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			},
		}
	} else {
		natsContainer = corev1.Container{
			Name:  "nats",
			Image: NATSImage,
			Args:  []string{"--jetstream"},
			Ports: []corev1.ContainerPort{{ContainerPort: 4222, Protocol: corev1.ProtocolTCP}},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			},
		}
	}

	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   natsDeploymentName(),
			Labels: map[string]string{LabelTeam: teamName, LabelRole: "nats"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{LabelTeam: teamName, LabelRole: "nats"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{LabelTeam: teamName, LabelRole: "nats"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{natsContainer},
				},
			},
		},
	}

	_, err = k.clientset.AppsV1().Deployments(namespace).Create(ctx, dep, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating nats deployment: %w", err)
	}

	// Create NATS ClusterIP service.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   natsServiceName(),
			Labels: map[string]string{LabelTeam: teamName, LabelRole: "nats"},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{LabelTeam: teamName, LabelRole: "nats"},
			Ports: []corev1.ServicePort{{
				Port:     4222,
				Protocol: corev1.ProtocolTCP,
			}},
			Type: corev1.ServiceTypeClusterIP,
		},
	}

	_, err = k.clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating nats service: %w", err)
	}

	// Wait for NATS deployment to be ready.
	slog.Info("waiting for nats deployment to be ready", "namespace", namespace)
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		d, err := k.clientset.AppsV1().Deployments(namespace).Get(ctx, natsDeploymentName(), metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return d.Status.ReadyReplicas >= 1, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for nats to be ready: %w", err)
	}

	slog.Info("nats is ready", "namespace", namespace)
	return nil
}

// DeployAgent creates a Pod for the agent in the team's namespace.
func (k *K8sRuntime) DeployAgent(ctx context.Context, config AgentConfig) (*AgentInstance, error) {
	if err := validateName(config.TeamName); err != nil {
		return nil, fmt.Errorf("invalid team name: %w", err)
	}
	if err := validateName(config.Name); err != nil {
		return nil, fmt.Errorf("invalid agent name: %w", err)
	}

	ns := teamNamespaceName(config.TeamName)
	podName := agentPodName(config.Name)
	img := config.Image
	if img == "" {
		img = DefaultAgentImage
	}

	slog.Info("deploying k8s agent", "agent", config.Name, "team", config.TeamName, "namespace", ns)

	// Ensure API key secret exists in namespace.
	if err := k.ensureAPIKeySecret(ctx, ns); err != nil {
		return nil, fmt.Errorf("ensuring api key secret: %w", err)
	}

	// Build env vars.
	permJSON, _ := json.Marshal(config.Permissions)
	env := []corev1.EnvVar{
		{Name: "AGENT_NAME", Value: config.Name},
		{Name: "TEAM_NAME", Value: config.TeamName},
		{Name: "NATS_URL", Value: config.NATSUrl},
		{Name: "AGENT_ROLE", Value: config.Role},
		{Name: "AGENT_PERMISSIONS", Value: string(permJSON)},
		{Name: "ANTHROPIC_API_KEY_FILE", Value: "/run/secrets/anthropic_api_key"},
	}

	// Build resource requirements.
	resources := corev1.ResourceRequirements{}
	if config.Resources.Memory != "" || config.Resources.CPU != "" {
		resources.Requests = corev1.ResourceList{}
		resources.Limits = corev1.ResourceList{}
		if config.Resources.Memory != "" {
			mem := resource.MustParse(config.Resources.Memory)
			resources.Requests[corev1.ResourceMemory] = mem
			resources.Limits[corev1.ResourceMemory] = mem
		}
		if config.Resources.CPU != "" {
			cpu := resource.MustParse(config.Resources.CPU)
			resources.Requests[corev1.ResourceCPU] = cpu
			resources.Limits[corev1.ResourceCPU] = cpu
		}
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels: map[string]string{
				LabelTeam:  config.TeamName,
				LabelAgent: config.Name,
				LabelRole:  config.Role,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:      "agent",
					Image:     img,
					Env:       env,
					Resources: resources,
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
						{Name: "api-key", MountPath: "/run/secrets", ReadOnly: true},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "workspace",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: workspacePVCName(),
						},
					},
				},
				{
					Name: "api-key",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: apiKeySecretName(),
							Items: []corev1.KeyToPath{
								{Key: "api-key", Path: "anthropic_api_key"},
							},
						},
					},
				},
			},
		},
	}

	created, err := k.clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating agent pod: %w", err)
	}

	agentID := ns + "/" + created.Name
	slog.Info("k8s agent pod created", "id", agentID, "agent", config.Name)

	return &AgentInstance{
		ID:     agentID,
		Name:   config.Name,
		Status: "running",
	}, nil
}

// StopAgent deletes the agent pod.
func (k *K8sRuntime) StopAgent(ctx context.Context, id string) error {
	ns, podName, err := parseAgentID(id)
	if err != nil {
		return err
	}
	return k.clientset.CoreV1().Pods(ns).Delete(ctx, podName, metav1.DeleteOptions{})
}

// RemoveAgent deletes the agent pod. In Kubernetes, this is equivalent to StopAgent.
func (k *K8sRuntime) RemoveAgent(ctx context.Context, id string) error {
	return k.StopAgent(ctx, id)
}

// GetStatus returns the current status of an agent pod.
func (k *K8sRuntime) GetStatus(ctx context.Context, id string) (*AgentStatus, error) {
	ns, podName, err := parseAgentID(id)
	if err != nil {
		return nil, err
	}

	pod, err := k.clientset.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting pod %s: %w", id, err)
	}

	status := podPhaseToStatus(pod.Status.Phase)
	startedAt := pod.CreationTimestamp.Time

	return &AgentStatus{
		ID:        id,
		Name:      pod.Labels[LabelAgent],
		Status:    status,
		StartedAt: startedAt,
	}, nil
}

// StreamLogs returns a reader for the agent pod's log stream.
func (k *K8sRuntime) StreamLogs(ctx context.Context, id string) (io.ReadCloser, error) {
	ns, podName, err := parseAgentID(id)
	if err != nil {
		return nil, err
	}

	req := k.clientset.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
		Follow: true,
	})
	return req.Stream(ctx)
}

// TeardownInfra deletes the entire team namespace, which cascades to all resources within it.
func (k *K8sRuntime) TeardownInfra(ctx context.Context, teamName string) error {
	ns := teamNamespaceName(teamName)
	slog.Info("tearing down k8s team infrastructure", "team", teamName, "namespace", ns)

	err := k.clientset.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting namespace %s: %w", ns, err)
	}

	slog.Info("k8s team infrastructure torn down", "team", teamName)
	return nil
}

// ensureAPIKeySecret creates the Kubernetes Secret holding the Anthropic API key
// if it doesn't already exist in the given namespace.
func (k *K8sRuntime) ensureAPIKeySecret(ctx context.Context, namespace string) error {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiKeySecretName(),
		},
		StringData: map[string]string{
			"api-key": apiKey,
		},
	}

	_, err := k.clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating api key secret: %w", err)
	}
	return nil
}

// podPhaseToStatus converts a Kubernetes PodPhase to the internal status string.
func podPhaseToStatus(phase corev1.PodPhase) string {
	switch phase {
	case corev1.PodRunning:
		return "running"
	case corev1.PodFailed:
		return "error"
	default:
		return "stopped"
	}
}
