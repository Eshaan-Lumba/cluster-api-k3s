/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controllers

import (
	"sigs.k8s.io/cluster-api/util/collections"

	"github.com/k3s-io/cluster-api-k3s/pkg/k3s"
	"github.com/k3s-io/cluster-api-k3s/pkg/machinefilters"
)

// structuralVersionOnlyGate checks the cheap structural preconditions: exactly
// one desired replica, exactly one existing Machine, exactly one Machine
// needing rollout, and that Machine's version differs from KCP.Spec.Version.
// These are the parts that can be exercised in a pure unit test.
func structuralVersionOnlyGate(cp *k3s.ControlPlane, needRollout collections.Machines) bool {
	if cp == nil || cp.KCP == nil {
		return false
	}
	if cp.KCP.Spec.Replicas == nil || *cp.KCP.Spec.Replicas != 1 {
		return false
	}
	if len(cp.Machines) != 1 || needRollout.Len() != 1 {
		return false
	}
	m := needRollout.Oldest()
	if m == nil || m.Spec.Version == "" || cp.KCP.Spec.Version == "" {
		return false
	}
	// Version differs == the version filter does NOT match.
	return !machinefilters.MatchesKubernetesVersion(cp.KCP.Spec.Version)(m)
}

// isSingleReplicaVersionOnlyRollout returns true only when the structural gate
// holds AND the ONLY reason the Machine needs rollout is its version — i.e.
// its bootstrap config and infra template still match KCP. If those also
// differ, an in-place version bump is unsafe and we must fall back to recreate.
func isSingleReplicaVersionOnlyRollout(cp *k3s.ControlPlane, needRollout collections.Machines) bool {
	if !structuralVersionOnlyGate(cp, needRollout) {
		return false
	}
	m := needRollout.Oldest()
	bootstrapMatches := machinefilters.MatchesKThreesBootstrapConfig(cp.KthreesConfigs, cp.KCP)(m)
	templateMatches := machinefilters.MatchesTemplateClonedFrom(cp.InfraResources, cp.KCP)(m)
	return bootstrapMatches && templateMatches
}
