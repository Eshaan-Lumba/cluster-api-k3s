/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controllers

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
)

// CAPI in-place update coordination annotations. Defined locally because the
// fork pins sigs.k8s.io/cluster-api v1.11.5, which predates these constants.
// The string values are wire-identical to CAPI v1.12 (which runs in the
// management cluster and acts on them).
const (
	updateInProgressAnnotation = "in-place-updates.internal.cluster.x-k8s.io/update-in-progress"
	pendingHooksAnnotation     = "runtime.cluster.x-k8s.io/pending-hooks"
	updateMachineHookName      = "UpdateMachine"
)

func addToCommaSeparatedList(list string, items ...string) string {
	set := sets.New[string](strings.Split(list, ",")...)
	set.Insert(items...)
	set.Delete("")
	return strings.Join(sets.List(set), ",")
}

func isInCommaSeparatedList(list, item string) bool {
	return sets.New[string](strings.Split(list, ",")...).Has(item)
}
