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
	"fmt"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/k3s-io/cluster-api-k3s/pkg/k3s"
)

// inplaceUpdateInProgress reports whether any Machine is mid in-place update
// (update-in-progress annotation present, or UpdateMachine hook still pending).
func inplaceUpdateInProgress(machines collections.Machines) bool {
	for _, m := range machines {
		if m == nil {
			continue
		}
		if _, ok := m.Annotations[updateInProgressAnnotation]; ok {
			return true
		}
		if isInCommaSeparatedList(m.Annotations[pendingHooksAnnotation], updateMachineHookName) {
			return true
		}
	}
	return false
}

// triggerInPlaceVersionUpdate marks a single control-plane Machine (and its
// InfraMachine + KThreesConfig) for in-place update, then marks the
// UpdateMachine hook pending so the (v1.12) management-cluster Machine
// controller takes over. A-minimal: annotations only, no spec mutation,
// plain client.Patch (no SSA, no RuntimeClient).
func (r *KThreesControlPlaneReconciler) triggerInPlaceVersionUpdate(ctx context.Context, cp *k3s.ControlPlane, machine *clusterv1.Machine) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	infra, ok := cp.InfraResources[machine.Name]
	if !ok || infra == nil {
		log.Info("InfraMachine not yet available, requeueing", "machine", machine.Name)
		return ctrl.Result{Requeue: true}, nil
	}
	cfg, ok := cp.KthreesConfigs[machine.Name]
	if !ok || cfg == nil {
		log.Info("KThreesConfig not yet available, requeueing", "machine", machine.Name)
		return ctrl.Result{Requeue: true}, nil
	}

	if err := setInProgressAnnotation(ctx, r.Client, infra); err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to annotate InfraMachine for %s", machine.Name)
	}
	if err := setInProgressAnnotation(ctx, r.Client, cfg); err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to annotate KThreesConfig for %s", machine.Name)
	}

	// Set the desired version AND both annotations on the Machine in a SINGLE atomic patch.
	//   - Version: advance spec.version to the desired version so the UpdateMachine hook
	//     (CAPE) targets the new version. The faithful flow (KCP/CAPRKE2) recomputes and
	//     SSA-applies the full desired Machine/InfraMachine/BootstrapConfig; our guard
	//     restricts to version-only single-node rollouts, so version is the sole delta and
	//     a targeted patch suffices (the v1.12 Machine webhook allows version mutation;
	//     it only validates semver).
	//   - Annotations in one patch: splitting them risks a wedge: if the hook patch fails
	//     after the in-progress patch succeeded, inplaceUpdateInProgress would return true
	//     on every later reconcile and short-circuit before we get back here to set the
	//     hook, so the handoff to the v1.12 Machine controller would never happen.
	orig := machine.DeepCopy()
	if machine.Annotations == nil {
		machine.Annotations = map[string]string{}
	}
	machine.Spec.Version = cp.KCP.Spec.Version
	machine.Annotations[updateInProgressAnnotation] = ""
	machine.Annotations[pendingHooksAnnotation] = addToCommaSeparatedList(machine.Annotations[pendingHooksAnnotation], updateMachineHookName)
	if err := r.Client.Patch(ctx, machine, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to set version/annotations on Machine %s and mark UpdateMachine hook pending", machine.Name)
	}

	if r.recorder != nil {
		r.recorder.Event(machine, corev1.EventTypeNormal, "InPlaceUpgradeTriggered",
			fmt.Sprintf("Triggered in-place upgrade to %s", cp.KCP.Spec.Version))
	}
	log.Info("Triggered in-place version update", "machine", machine.Name, "targetVersion", cp.KCP.Spec.Version)
	return ctrl.Result{}, nil
}

// setInProgressAnnotation sets updateInProgressAnnotation on obj via a plain
// merge patch, no-op if already present.
func setInProgressAnnotation(ctx context.Context, c client.Client, obj client.Object) error {
	if _, ok := obj.GetAnnotations()[updateInProgressAnnotation]; ok {
		return nil
	}
	orig := obj.DeepCopyObject().(client.Object)
	ann := obj.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[updateInProgressAnnotation] = ""
	obj.SetAnnotations(ann)
	return c.Patch(ctx, obj, client.MergeFrom(orig))
}
