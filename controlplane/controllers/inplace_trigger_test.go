/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controllers

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bootstrapv1 "github.com/k3s-io/cluster-api-k3s/bootstrap/api/v1beta2"
	controlplanev1 "github.com/k3s-io/cluster-api-k3s/controlplane/api/v1beta2"
	"github.com/k3s-io/cluster-api-k3s/pkg/k3s"
)

func newTestScheme(g Gomega) *runtime.Scheme {
	scheme := runtime.NewScheme()
	g.Expect(clusterv1.AddToScheme(scheme)).To(Succeed())
	g.Expect(bootstrapv1.AddToScheme(scheme)).To(Succeed())
	g.Expect(controlplanev1.AddToScheme(scheme)).To(Succeed())
	return scheme
}

func TestTriggerInPlaceVersionUpdate_SetsVersionAndAnnotations(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	m := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-0", Namespace: "ns"},
		Spec:       clusterv1.MachineSpec{Version: "v1.30.6+k3s1"},
	}
	infra := &unstructured.Unstructured{}
	infra.SetGroupVersionKind(clusterv1.GroupVersion.WithKind("DockerMachine"))
	infra.SetName("cp-0-infra")
	infra.SetNamespace("ns")
	cfg := &bootstrapv1.KThreesConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-0-cfg", Namespace: "ns"},
	}

	scheme := newTestScheme(g)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(m, infra, cfg).Build()

	cp := &k3s.ControlPlane{
		KCP: &controlplanev1.KThreesControlPlane{
			Spec: controlplanev1.KThreesControlPlaneSpec{
				Replicas: ptr.To(int32(1)),
				Version:  "v1.31.6+k3s1",
			},
		},
		Machines:       collections.FromMachines(m),
		InfraResources: map[string]*unstructured.Unstructured{"cp-0": infra},
		KthreesConfigs: map[string]*bootstrapv1.KThreesConfig{"cp-0": cfg},
	}

	r := &KThreesControlPlaneReconciler{Client: c}
	res, err := r.triggerInPlaceVersionUpdate(ctx, cp, m)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(res.Requeue).To(BeFalse())
	g.Expect(res.RequeueAfter).To(BeZero())

	gotM := &clusterv1.Machine{}
	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(m), gotM)).To(Succeed())
	g.Expect(gotM.Annotations).To(HaveKey(updateInProgressAnnotation))
	g.Expect(isInCommaSeparatedList(gotM.Annotations[pendingHooksAnnotation], updateMachineHookName)).To(BeTrue())
	// Version advanced to the desired (KCP) version so CAPE's UpdateMachine hook targets it,
	// not the Machine's stale current version.
	g.Expect(gotM.Spec.Version).To(Equal(cp.KCP.Spec.Version))

	gotCfg := &bootstrapv1.KThreesConfig{}
	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(cfg), gotCfg)).To(Succeed())
	g.Expect(gotCfg.Annotations).To(HaveKey(updateInProgressAnnotation))

	gotInfra := &unstructured.Unstructured{}
	gotInfra.SetGroupVersionKind(infra.GroupVersionKind())
	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(infra), gotInfra)).To(Succeed())
	g.Expect(gotInfra.GetAnnotations()).To(HaveKey(updateInProgressAnnotation))

	// Re-entrancy: subsequent reconcile sees the in-progress state.
	g.Expect(inplaceUpdateInProgress(collections.FromMachines(gotM))).To(BeTrue())
}

func TestTriggerInPlaceVersionUpdate_RequeuesWhenInfraMissing(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	m := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-0", Namespace: "ns"},
		Spec:       clusterv1.MachineSpec{Version: "v1.30.6+k3s1"},
	}
	cfg := &bootstrapv1.KThreesConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-0-cfg", Namespace: "ns"},
	}

	scheme := newTestScheme(g)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(m, cfg).Build()

	cp := &k3s.ControlPlane{
		KCP: &controlplanev1.KThreesControlPlane{
			Spec: controlplanev1.KThreesControlPlaneSpec{
				Replicas: ptr.To(int32(1)),
				Version:  "v1.31.6+k3s1",
			},
		},
		Machines:       collections.FromMachines(m),
		InfraResources: map[string]*unstructured.Unstructured{}, // missing
		KthreesConfigs: map[string]*bootstrapv1.KThreesConfig{"cp-0": cfg},
	}

	r := &KThreesControlPlaneReconciler{Client: c}
	res, err := r.triggerInPlaceVersionUpdate(ctx, cp, m)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(res.Requeue).To(BeTrue())

	// Machine should NOT have been annotated yet.
	gotM := &clusterv1.Machine{}
	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(m), gotM)).To(Succeed())
	g.Expect(gotM.Annotations).ToNot(HaveKey(updateInProgressAnnotation))
}

func TestInplaceUpdateInProgress(t *testing.T) {
	tests := []struct {
		name     string
		machine  *clusterv1.Machine
		expected bool
	}{
		{
			name:     "no annotations -> false",
			machine:  &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "m"}},
			expected: false,
		},
		{
			name: "update-in-progress annotation present -> true",
			machine: &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{
				Name:        "m",
				Annotations: map[string]string{updateInProgressAnnotation: ""},
			}},
			expected: true,
		},
		{
			name: "pending UpdateMachine hook -> true",
			machine: &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{
				Name:        "m",
				Annotations: map[string]string{pendingHooksAnnotation: "Other,UpdateMachine"},
			}},
			expected: true,
		},
		{
			name: "other pending hook only -> false",
			machine: &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{
				Name:        "m",
				Annotations: map[string]string{pendingHooksAnnotation: "OtherHook"},
			}},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(inplaceUpdateInProgress(collections.FromMachines(tc.machine))).To(Equal(tc.expected))
		})
	}
}

func TestAddToCommaSeparatedList(t *testing.T) {
	tests := []struct {
		name  string
		list  string
		items []string
		want  string
	}{
		{name: "empty list", list: "", items: []string{"a"}, want: "a"},
		{name: "dedupe", list: "a,b", items: []string{"b", "c"}, want: "a,b,c"},
		{name: "drops empties", list: "", items: []string{"", "x", ""}, want: "x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(addToCommaSeparatedList(tc.list, tc.items...)).To(Equal(tc.want))
		})
	}
}
