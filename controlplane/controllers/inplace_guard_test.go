/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controllers

import (
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"

	bootstrapv1 "github.com/k3s-io/cluster-api-k3s/bootstrap/api/v1beta2"
	controlplanev1 "github.com/k3s-io/cluster-api-k3s/controlplane/api/v1beta2"
	"github.com/k3s-io/cluster-api-k3s/pkg/k3s"
)

func machineWithVersion(name, version string) *clusterv1.Machine {
	return &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec:       clusterv1.MachineSpec{Version: version},
	}
}

func TestStructuralVersionOnlyGate(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(cp *k3s.ControlPlane, m *clusterv1.Machine)
		expected bool
	}{
		{
			name:     "version differs, single replica, single machine -> true",
			mutate:   func(*k3s.ControlPlane, *clusterv1.Machine) {},
			expected: true,
		},
		{
			name: "replicas != 1 -> false",
			mutate: func(cp *k3s.ControlPlane, _ *clusterv1.Machine) {
				cp.KCP.Spec.Replicas = ptr.To(int32(3))
			},
			expected: false,
		},
		{
			name: "version equal -> false",
			mutate: func(_ *k3s.ControlPlane, m *clusterv1.Machine) {
				m.Spec.Version = "v1.31.6+k3s1"
			},
			expected: false,
		},
		{
			name: "replicas nil -> false",
			mutate: func(cp *k3s.ControlPlane, _ *clusterv1.Machine) {
				cp.KCP.Spec.Replicas = nil
			},
			expected: false,
		},
		{
			name: "empty machine version -> false",
			mutate: func(_ *k3s.ControlPlane, m *clusterv1.Machine) {
				m.Spec.Version = ""
			},
			expected: false,
		},
		{
			name: "empty kcp version -> false",
			mutate: func(cp *k3s.ControlPlane, _ *clusterv1.Machine) {
				cp.KCP.Spec.Version = ""
			},
			expected: false,
		},
		{
			name: "two machines in collection -> false",
			mutate: func(cp *k3s.ControlPlane, _ *clusterv1.Machine) {
				other := machineWithVersion("cp-1", "v1.30.6+k3s1")
				cp.Machines.Insert(other)
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			kcp := &controlplanev1.KThreesControlPlane{
				Spec: controlplanev1.KThreesControlPlaneSpec{
					Replicas: ptr.To(int32(1)),
					Version:  "v1.31.6+k3s1",
				},
			}
			m := machineWithVersion("cp-0", "v1.30.6+k3s1")
			cp := &k3s.ControlPlane{
				KCP:            kcp,
				Machines:       collections.FromMachines(m),
				InfraResources: map[string]*unstructured.Unstructured{},
				KthreesConfigs: map[string]*bootstrapv1.KThreesConfig{},
			}

			tc.mutate(cp, m)

			g.Expect(structuralVersionOnlyGate(cp, cp.Machines)).To(Equal(tc.expected))
		})
	}
}

// TestIsSingleReplicaVersionOnlyRollout exercises the full guard, including
// the negative branches where the bootstrap or infra-template differ from
// KCP (which must force a recreate rather than an in-place version bump).
// Note: with empty infra/config maps the inner machinefilters take the
// safety-default path and return true, so a structurally-valid version-only
// diff passes the full guard.
func TestIsSingleReplicaVersionOnlyRollout(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(cp *k3s.ControlPlane, m *clusterv1.Machine)
		expected bool
	}{
		{
			name:     "version-only diff, empty filter maps -> true (safety defaults)",
			mutate:   func(*k3s.ControlPlane, *clusterv1.Machine) {},
			expected: true,
		},
		{
			name: "structural gate fails -> false",
			mutate: func(cp *k3s.ControlPlane, _ *clusterv1.Machine) {
				cp.KCP.Spec.Replicas = ptr.To(int32(3))
			},
			expected: false,
		},
		{
			// Case A: defined bootstrap ref + a KThreesConfig present in
			// the map whose Spec differs from KCP's KThreesConfigSpec
			// (PreK3sCommands), so MatchesKThreesBootstrapConfig returns
			// false and the guard must reject in-place upgrade.
			name: "bootstrap config differs -> false",
			mutate: func(cp *k3s.ControlPlane, m *clusterv1.Machine) {
				m.Spec.Bootstrap.ConfigRef = clusterv1.ContractVersionedObjectReference{
					APIGroup: "bootstrap.cluster.x-k8s.io",
					Kind:     "KThreesConfig",
					Name:     "cp-0-cfg",
				}
				cp.KthreesConfigs[m.Name] = &bootstrapv1.KThreesConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "cp-0-cfg", Namespace: "ns"},
					Spec: bootstrapv1.KThreesConfigSpec{
						PreK3sCommands: []string{"echo different"},
					},
				}
			},
			expected: false,
		},
		{
			// Case B: infra Unstructured cloned-from annotations point at a
			// DIFFERENT template than KCP.Spec.MachineTemplate.InfrastructureRef,
			// so MatchesTemplateClonedFrom returns false and the guard must
			// reject in-place upgrade.
			name: "infra template clonedFrom name differs -> false",
			mutate: func(cp *k3s.ControlPlane, m *clusterv1.Machine) {
				cp.KCP.Spec.MachineTemplate.InfrastructureRef = corev1.ObjectReference{
					Name:       "kcp-infra-template",
					Kind:       "DockerMachineTemplate",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				}
				u := &unstructured.Unstructured{}
				u.SetGroupVersionKind(clusterv1.GroupVersion.WithKind("DockerMachine"))
				u.SetName(m.Name + "-infra")
				u.SetNamespace("ns")
				u.SetAnnotations(map[string]string{
					clusterv1beta1.TemplateClonedFromNameAnnotation:      "some-other-template",
					clusterv1beta1.TemplateClonedFromGroupKindAnnotation: "DockerMachineTemplate.infrastructure.cluster.x-k8s.io",
				})
				cp.InfraResources[m.Name] = u
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			kcp := &controlplanev1.KThreesControlPlane{
				Spec: controlplanev1.KThreesControlPlaneSpec{
					Replicas: ptr.To(int32(1)),
					Version:  "v1.31.6+k3s1",
				},
			}
			m := machineWithVersion("cp-0", "v1.30.6+k3s1")
			cp := &k3s.ControlPlane{
				KCP:            kcp,
				Machines:       collections.FromMachines(m),
				InfraResources: map[string]*unstructured.Unstructured{},
				KthreesConfigs: map[string]*bootstrapv1.KThreesConfig{},
			}

			tc.mutate(cp, m)

			g.Expect(isSingleReplicaVersionOnlyRollout(cp, cp.Machines)).To(Equal(tc.expected))
		})
	}
}
