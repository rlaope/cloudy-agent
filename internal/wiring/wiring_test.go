package wiring

import (
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/permission"
)

// TestBuildRegistry_NoKube verifies that BuildRegistry returns a usable
// registry containing jvm/py/gpu tools even when no kubeconfig is available.
func TestBuildRegistry_NoKube(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from real kubeconfig

	opts := Options{
		KubeconfigPath: "/nonexistent/kubeconfig",
		ContextName:    "",
		PromEndpoints:  nil,
	}

	reg, err := BuildRegistry(opts)
	if reg == nil {
		t.Fatal("BuildRegistry returned nil registry")
	}

	if err == nil {
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
	}

	reg, err := BuildRegistry(opts)
	if reg == nil {
		t.Fatal("BuildRegistry returned nil registry")
	}

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

func TestRebuildUsesExplicitProfile(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	reg, _ := Rebuild(config.Default(), RebuildOpts{
		KubeconfigPath: "/nonexistent/kubeconfig",
		Profile: &permission.Profile{
			Name: "chatops-route",
			Tools: permission.Tools{
				Allow: []string{"memory.record"},
			},
		},
	})
	if _, ok := reg.Get("memory.record"); !ok {
		t.Fatal("expected memory.record to survive explicit profile filter")
	}
	if _, ok := reg.Get("jvm.jstat_gc"); ok {
		t.Fatal("explicit profile was not applied; jvm.jstat_gc should be filtered out")
	}
}

func TestRebuildDoesNotLoadActiveProfileUnlessRequested(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	profile := &permission.Profile{
		Name: "active-narrow",
		Tools: permission.Tools{
			Allow: []string{"memory.record"},
		},
	}
	if err := permission.Save(profile); err != nil {
		t.Fatalf("Save profile: %v", err)
	}
	if err := permission.SetActive(profile.Name); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	reg, _ := Rebuild(config.Default(), RebuildOpts{KubeconfigPath: "/nonexistent/kubeconfig"})
	if _, ok := reg.Get("jvm.jstat_gc"); !ok {
		t.Fatal("active profile was applied despite UseActiveProfile=false")
	}

	reg, _ = Rebuild(config.Default(), RebuildOpts{KubeconfigPath: "/nonexistent/kubeconfig", UseActiveProfile: true})
	if _, ok := reg.Get("jvm.jstat_gc"); ok {
		t.Fatal("active profile was not applied when UseActiveProfile=true")
	}
}
