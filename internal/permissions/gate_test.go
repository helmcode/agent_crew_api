package permissions

import (
	"testing"
)

func TestGate_Evaluate_AllowsPermittedTool(t *testing.T) {
	gate := NewGate(PermissionConfig{
		AllowedTools: []string{"Bash", "Read"},
	})

	d := gate.Evaluate("Bash", "", nil)
	if !d.Allowed {
		t.Fatalf("expected allowed, got denied: %s", d.Reason)
	}
}

func TestGate_Evaluate_DeniesUnpermittedTool(t *testing.T) {
	gate := NewGate(PermissionConfig{
		AllowedTools: []string{"Read"},
	})

	d := gate.Evaluate("Bash", "", nil)
	if d.Allowed {
		t.Fatal("expected denied for unallowed tool")
	}
	if d.Reason != "tool not allowed: Bash" {
		t.Fatalf("unexpected reason: %s", d.Reason)
	}
}

func TestGate_Evaluate_EmptyAllowedToolsDeniesAll(t *testing.T) {
	gate := NewGate(PermissionConfig{})

	d := gate.Evaluate("AnyTool", "", nil)
	if d.Allowed {
		t.Fatal("empty AllowedTools should deny all tools (fail-closed)")
	}
}

func TestGate_Evaluate_ReDoSProtection(t *testing.T) {
	gate := NewGate(PermissionConfig{
		AllowedTools:    []string{"Bash"},
		AllowedCommands: []string{"*a*a*a*a*a*a*"},
	})

	// This pattern with multiple wildcards would cause exponential time
	// in the old recursive implementation. The iterative version handles it in O(n*m).
	// Pattern requires at least 6 'a' chars; value has 0 so should not match.
	d := gate.Evaluate("Bash", "bbbbbbbbbbbbbbbbbbbbbbbbbb", nil)
	if d.Allowed {
		t.Fatal("should not match when value has no 'a' characters")
	}

	// Matching case: value has enough 'a' characters.
	d = gate.Evaluate("Bash", "xaxaxaxaxaxax", nil)
	if !d.Allowed {
		t.Fatalf("should match, got denied: %s", d.Reason)
	}
}

func TestGate_Evaluate_DeniedCommandsTakePrecedence(t *testing.T) {
	gate := NewGate(PermissionConfig{
		AllowedTools:    []string{"Bash"},
		AllowedCommands: []string{"rm *"},
		DeniedCommands:  []string{"rm -rf *"},
	})

	d := gate.Evaluate("Bash", "rm -rf /", nil)
	if d.Allowed {
		t.Fatal("denied commands should take precedence over allowed")
	}
}

func TestGate_Evaluate_AllowedCommandGlob(t *testing.T) {
	gate := NewGate(PermissionConfig{
		AllowedTools:    []string{"Bash"},
		AllowedCommands: []string{"terraform *", "cat *"},
	})

	tests := []struct {
		command string
		allowed bool
	}{
		{"terraform plan", true},
		{"terraform apply -auto-approve", true},
		{"cat /etc/passwd", true},
		{"rm -rf /", false},
		{"kubectl delete pod foo", false},
	}

	for _, tt := range tests {
		d := gate.Evaluate("Bash", tt.command, nil)
		if d.Allowed != tt.allowed {
			t.Errorf("command %q: expected allowed=%v, got allowed=%v (reason: %s)",
				tt.command, tt.allowed, d.Allowed, d.Reason)
		}
	}
}

func TestGate_Evaluate_EmptyCommandSkipsCommandChecks(t *testing.T) {
	gate := NewGate(PermissionConfig{
		AllowedTools:    []string{"Read"},
		AllowedCommands: []string{"cat *"},
		DeniedCommands:  []string{"rm *"},
	})

	// Tools like Read have no command; should pass command checks.
	d := gate.Evaluate("Read", "", nil)
	if !d.Allowed {
		t.Fatalf("empty command should skip command checks, got denied: %s", d.Reason)
	}
}

func TestGate_Evaluate_FilesystemScope(t *testing.T) {
	gate := NewGate(PermissionConfig{
		AllowedTools:    []string{"Read", "Write"},
		FilesystemScope: "/workspace/terraform",
	})

	tests := []struct {
		paths   []string
		allowed bool
	}{
		{[]string{"/workspace/terraform/main.tf"}, true},
		{[]string{"/workspace/terraform"}, true},
		{[]string{"/workspace/terraform/modules/vpc/main.tf"}, true},
		{[]string{"/etc/passwd"}, false},
		{[]string{"/workspace/other/file.txt"}, false},
		{nil, true},   // no paths to check
		{[]string{}, true}, // empty paths slice
	}

	for _, tt := range tests {
		d := gate.Evaluate("Read", "", tt.paths)
		if d.Allowed != tt.allowed {
			t.Errorf("paths %v: expected allowed=%v, got allowed=%v (reason: %s)",
				tt.paths, tt.allowed, d.Allowed, d.Reason)
		}
	}
}

func TestGate_Evaluate_PathTraversalAttack(t *testing.T) {
	gate := NewGate(PermissionConfig{
		AllowedTools:    []string{"Read"},
		FilesystemScope: "/workspace",
	})

	attacks := []string{
		"/workspace/../etc/passwd",
		"/workspace/../../etc/shadow",
		"/workspace/./../../root/.ssh/id_rsa",
		"/workspace/subdir/../../../../etc/hosts",
	}

	for _, path := range attacks {
		d := gate.Evaluate("Read", "", []string{path})
		if d.Allowed {
			t.Errorf("path traversal attack should be denied: %s", path)
		}
	}
}

func TestGate_Evaluate_MultiplePathsAllMustBeInScope(t *testing.T) {
	gate := NewGate(PermissionConfig{
		AllowedTools:    []string{"Write"},
		FilesystemScope: "/workspace",
	})

	// One path in scope, one out of scope.
	d := gate.Evaluate("Write", "", []string{"/workspace/file.txt", "/etc/passwd"})
	if d.Allowed {
		t.Fatal("should deny when any path is out of scope")
	}
}

func TestGate_Evaluate_FullPipeline(t *testing.T) {
	gate := NewGate(PermissionConfig{
		AllowedTools:    []string{"Bash", "Read", "Write"},
		AllowedCommands: []string{"terraform *", "kubectl get *"},
		DeniedCommands:  []string{"terraform destroy *", "kubectl delete *"},
		FilesystemScope: "/workspace",
	})

	tests := []struct {
		name    string
		tool    string
		command string
		paths   []string
		allowed bool
	}{
		{"allowed terraform plan", "Bash", "terraform plan", nil, true},
		{"denied terraform destroy", "Bash", "terraform destroy -auto-approve", nil, false},
		{"allowed kubectl get", "Bash", "kubectl get pods", nil, true},
		{"denied kubectl delete", "Bash", "kubectl delete pod foo", nil, false},
		{"read in scope", "Read", "", []string{"/workspace/main.tf"}, true},
		{"read out of scope", "Read", "", []string{"/root/secrets"}, false},
		{"denied tool", "Glob", "", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := gate.Evaluate(tt.tool, tt.command, tt.paths)
			if d.Allowed != tt.allowed {
				t.Errorf("expected allowed=%v, got allowed=%v (reason: %s)",
					tt.allowed, d.Allowed, d.Reason)
			}
		})
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		match   bool
	}{
		{"terraform plan", "terraform plan", true},
		{"terraform plan", "terraform apply", false},
		{"terraform *", "terraform plan", true},
		{"terraform *", "terraform apply -auto-approve", true},
		{"terraform *", "kubectl get pods", false},
		{"*", "anything", true},
		{"*", "", true},
		{"cat *", "cat /etc/passwd", true},
		{"rm -rf *", "rm -rf /", true},
		{"rm -rf *", "rm /tmp/file", false},
		// Multiple wildcards.
		{"*terraform*", "run terraform plan", true},
		{"*terraform*", "just a test", false},
	}

	for _, tt := range tests {
		got := MatchPattern(tt.pattern, tt.value)
		if got != tt.match {
			t.Errorf("MatchPattern(%q, %q) = %v, want %v", tt.pattern, tt.value, got, tt.match)
		}
	}
}

func TestIsPathInScope(t *testing.T) {
	tests := []struct {
		path    string
		scope   string
		inScope bool
	}{
		{"/workspace/file.txt", "/workspace", true},
		{"/workspace", "/workspace", true},
		{"/workspace/sub/dir/file", "/workspace", true},
		{"/etc/passwd", "/workspace", false},
		{"/workspacex/file", "/workspace", false}, // prefix but not child
		{"", "/workspace", false},
		{"/workspace/file", "", false},
		{"", "", false},
		// Path traversal.
		{"/workspace/../etc/passwd", "/workspace", false},
		{"/workspace/sub/../../etc/passwd", "/workspace", false},
		// Dot components resolved.
		{"/workspace/./file.txt", "/workspace", true},
		{"/workspace/sub/../sub/file", "/workspace", true},
	}

	for _, tt := range tests {
		got := IsPathInScope(tt.path, tt.scope)
		if got != tt.inScope {
			t.Errorf("IsPathInScope(%q, %q) = %v, want %v", tt.path, tt.scope, got, tt.inScope)
		}
	}
}
