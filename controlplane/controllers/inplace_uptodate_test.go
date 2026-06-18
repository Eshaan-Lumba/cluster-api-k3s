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
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	controlplanev1 "github.com/k3s-io/cluster-api-k3s/controlplane/api/v1beta2"
	"github.com/k3s-io/cluster-api-k3s/pkg/k3s"
)

// TestReconcileMachinesUpToDateConditions verifies the per-Machine UpToDate
// condition is set True for machines that are up to date and False for machines
// that still need rollout. This is the signal the management cluster + AKS Arc
// operator read to detect in-place upgrade completion.
func TestReconcileMachinesUpToDateConditions(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	upToDate := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-uptodate", Namespace: "ns"},
		Spec:       clusterv1.MachineSpec{Version: "v1.31.6+k3s1"},
	}
	stale := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-stale", Namespace: "ns"},
		Spec:       clusterv1.MachineSpec{Version: "v1.30.6+k3s1"},
	}

	scheme := newTestScheme(g)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(upToDate, stale).
		WithStatusSubresource(upToDate, stale).
		Build()

	cp := &k3s.ControlPlane{
		KCP:      &controlplanev1.KThreesControlPlane{},
		Machines: collections.FromMachines(upToDate, stale),
	}
	// Only the stale machine needs rollout; the other is up to date.
	needRollout := collections.FromMachines(stale)

	r := &KThreesControlPlaneReconciler{Client: c}
	g.Expect(r.reconcileMachinesUpToDateConditions(ctx, cp, needRollout)).To(Succeed())

	gotUpToDate := &clusterv1.Machine{}
	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(upToDate), gotUpToDate)).To(Succeed())
	g.Expect(conditions.IsTrue(gotUpToDate, clusterv1.MachineUpToDateCondition)).To(BeTrue())
	g.Expect(conditions.Get(gotUpToDate, clusterv1.MachineUpToDateCondition).Reason).To(Equal(clusterv1.MachineUpToDateReason))

	gotStale := &clusterv1.Machine{}
	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(stale), gotStale)).To(Succeed())
	g.Expect(conditions.IsFalse(gotStale, clusterv1.MachineUpToDateCondition)).To(BeTrue())
	g.Expect(conditions.Get(gotStale, clusterv1.MachineUpToDateCondition).Reason).To(Equal(clusterv1.MachineNotUpToDateReason))
}

// TestReconcileMachinesUpToDateConditions_NoRollout verifies that when no machine
// needs rollout (the steady state after an in-place upgrade completes) every
// owned machine flips to UpToDate=True — the transition that unblocks the
// operator's addon-upgrade gate.
func TestReconcileMachinesUpToDateConditions_NoRollout(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	m := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-0", Namespace: "ns"},
		Spec:       clusterv1.MachineSpec{Version: "v1.31.6+k3s1"},
	}

	scheme := newTestScheme(g)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(m).
		WithStatusSubresource(m).
		Build()

	cp := &k3s.ControlPlane{
		KCP:      &controlplanev1.KThreesControlPlane{},
		Machines: collections.FromMachines(m),
	}

	r := &KThreesControlPlaneReconciler{Client: c}
	// Empty needRollout: nothing is stale.
	g.Expect(r.reconcileMachinesUpToDateConditions(ctx, cp, collections.Machines{})).To(Succeed())

	got := &clusterv1.Machine{}
	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(m), got)).To(Succeed())
	g.Expect(conditions.IsTrue(got, clusterv1.MachineUpToDateCondition)).To(BeTrue())

	// Idempotent: a second pass with the condition already True is a no-op and does not error.
	g.Expect(r.reconcileMachinesUpToDateConditions(ctx, cp, collections.Machines{})).To(Succeed())
}
