package wiring

import (
	"testing"
)

// TestBuildRegistry_NoKube verifies that BuildRegistry returns a usable
// registry containing jvm/py/gpu tools even when no kubeconfig is available.
func TestBuildRegistry_NoKube(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from real kubeconfig

	opts := Options{
		KubeconfigPath: "/nonexistent/kubeconfig",
		ContextName:    "",
		PromEndpoints:  nil,
		EnableJVM:      true,
		EnablePython:   true,
		EnableGPU:      true,
	}

	reg, err := BuildRegistry(opts)
	if reg == nil {
		t.Fatal("BuildRegistry returned nil registry")
	}

	// err should be a KubeWarning (non-fatal), not nil and not a hard error.
	if err == nil {
		// May be nil if there happens to be a real kubeconfig — allow that.
		t.Log("BuildRegistry returned nil error (kubeconfig found or in-cluster)")
	} else {
		if _, ok := err.(*KubeWarning); !ok {
			t.Errorf("expected *KubeWarning, got %T: %v", err, err)
		}
	}

	wantTools := []string{
		"jvm.jstat_gc",
		"jvm.jcmd_gc",
		"jvm.jcmd_thread_dump",
		"jvm.async_profile",
		"py.spy_dump",
		"py.spy_top_snapshot",
		"gpu.nvidia_smi",
	}

	for _, name := range wantTools {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("registry missing expected tool %q", name)
		}
	}
}

// TestBuildRegistry_K8sToolsAbsentWithoutKube ensures k8s tools are NOT
// registered when the kube client cannot be constructed.
func TestBuildRegistry_K8sToolsAbsentWithoutKube(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	opts := Options{
		KubeconfigPath: "/nonexistent/kubeconfig",
		EnableJVM:      true,
		EnablePython:   true,
		EnableGPU:      true,
	}

	reg, err := BuildRegistry(opts)
	if reg == nil {
		t.Fatal("BuildRegistry returned nil registry")
	}

	// If err is nil, kubeconfig was found — skip k8s-absence check.
	if err == nil {
		t.Skip("real kubeconfig found; skipping k8s-absence assertion")
	}

	k8sTools := []string{
		"k8s.list_pods",
		"k8s.list_nodes",
		"k8s.describe_pod",
	}
	for _, name := range k8sTools {
		if _, ok := reg.Get(name); ok {
			t.Errorf("registry should NOT contain %q when kube client fails", name)
		}
	}
}
