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

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"

	"github.com/k3s-io/cluster-api-k3s/pkg/k3s"
)

// reconcileMachinesUpToDateConditions reports the per-Machine UpToDate condition
// (clusterv1.MachineUpToDateCondition) on every owned control-plane Machine.
//
// Upstream KCP sets this v1beta2 core condition for kubeadm control-plane
// Machines; CACP3 (the K3s control-plane provider) historically only sets the
// KCP-level MachinesSpecUpToDate condition and never the per-Machine one. That
// gap matters for the A-minimal in-place upgrade: the node upgrades in place,
// but readers that key off the per-Machine condition never observe completion —
// core CAPI's ControlPlaneMachinesUpToDate aggregation stays Unknown, and the
// AKS Arc operator's completion tracking (emitNodeUpgradeTelemetry ->
// UpgradeStatus.NodesCompleted) never records the node, so the addon-upgrade
// gate never opens and the ARM-level upgrade hangs until it times out.
//
// A Machine is up to date when it is NOT in needRollout — i.e. its version,
// bootstrap config, and infra template all match the desired KThreesControlPlane
// spec (the same filters MachinesNeedingRollout applies).
func (r *KThreesControlPlaneReconciler) reconcileMachinesUpToDateConditions(ctx context.Context, controlPlane *k3s.ControlPlane, needRollout collections.Machines) error {
	notUpToDate := make(map[string]struct{}, len(needRollout))
	for _, m := range needRollout {
		notUpToDate[m.Name] = struct{}{}
	}

	var errs []error
	for _, machine := range controlPlane.Machines {
		desired := metav1.Condition{
			Type:   clusterv1.MachineUpToDateCondition,
			Status: metav1.ConditionTrue,
			Reason: clusterv1.MachineUpToDateReason,
		}
		if _, stale := notUpToDate[machine.Name]; stale {
			desired.Status = metav1.ConditionFalse
			desired.Reason = clusterv1.MachineNotUpToDateReason
		}

		// Skip the write when the condition already matches to avoid needless
		// patches (and the requeues they trigger) on steady-state reconciles.
		if existing := conditions.Get(machine, clusterv1.MachineUpToDateCondition); existing != nil &&
			existing.Status == desired.Status && existing.Reason == desired.Reason {
			continue
		}

		patchHelper, err := patch.NewHelper(machine, r.Client)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to create patch helper for machine %s", machine.Name))
			continue
		}
		conditions.Set(machine, desired)
		if err := patchHelper.Patch(ctx, machine, patch.WithOwnedConditions{Conditions: []string{clusterv1.MachineUpToDateCondition}}); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to patch UpToDate condition on machine %s", machine.Name))
		}
	}
	return kerrors.NewAggregate(errs)
}
