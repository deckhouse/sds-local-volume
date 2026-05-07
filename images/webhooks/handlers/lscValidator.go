/*
Copyright 2025 Flant JSC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package handlers

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/slok/kubewebhook/v2/pkg/model"
	kwhvalidating "github.com/slok/kubewebhook/v2/pkg/webhook/validating"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func LSCValidate(ctx context.Context, _ *model.AdmissionReview, obj metav1.Object) (*kwhvalidating.ValidatorResult, error) {
	lsc, ok := obj.(*slv.LocalStorageClass)
	if !ok {
		// If not a storage class just continue the validation chain(if there is one) and do nothing.
		return &kwhvalidating.ValidatorResult{}, nil
	}

	// Validate that either LVM or RawFile is specified, but not both
	if lsc.Spec.LVM != nil && lsc.Spec.RawFile != nil {
		errMsg := "LocalStorageClass must have either lvm or rawFile configuration, not both"
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
	}

	if lsc.Spec.LVM == nil && lsc.Spec.RawFile == nil {
		errMsg := "LocalStorageClass must have either lvm or rawFile configuration"
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
	}

	// RawFile validation
	if lsc.Spec.RawFile != nil {
		return validateRawFile(ctx, lsc)
	}

	// LVM validation
	return validateLVM(ctx, lsc)
}

// nodeExistenceChecker is overridable from tests to avoid spinning up a real
// API client. Production code uses the default kube client built by
// NewKubeClient.
var nodeExistenceChecker = defaultNodeExistenceChecker

// defaultNodeExistenceChecker returns the set of node names from `wanted`
// that DO NOT currently exist in the cluster, plus a hard error if the API
// call itself failed (which the caller MUST treat as fail-open: the cluster
// might be temporarily unreachable, and we don't want webhook outages to
// block LSC edits indefinitely).
func defaultNodeExistenceChecker(ctx context.Context, wanted []string) (missing []string, err error) {
	cl, cerr := NewKubeClient("")
	if cerr != nil {
		return nil, fmt.Errorf("build kube client: %w", cerr)
	}
	for _, name := range wanted {
		node := &corev1.Node{}
		gerr := cl.Get(ctx, types.NamespacedName{Name: name}, node)
		switch {
		case gerr == nil:
			continue
		case kerrors.IsNotFound(gerr):
			missing = append(missing, name)
		default:
			return nil, fmt.Errorf("get node %q: %w", name, gerr)
		}
	}
	return missing, nil
}

func validateRawFile(ctx context.Context, lsc *slv.LocalStorageClass) (*kwhvalidating.ValidatorResult, error) {
	klog.Infof("Validating RawFile LocalStorageClass: %s", lsc.Name)

	if len(lsc.Spec.RawFile.Nodes) == 0 {
		return &kwhvalidating.ValidatorResult{Valid: true}, nil
	}

	nodeNames := make(map[string]struct{}, len(lsc.Spec.RawFile.Nodes))
	wanted := make([]string, 0, len(lsc.Spec.RawFile.Nodes))
	for _, node := range lsc.Spec.RawFile.Nodes {
		if node.Name == "" {
			errMsg := "RawFile node name must not be empty"
			klog.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
		}
		if _, exists := nodeNames[node.Name]; exists {
			errMsg := fmt.Sprintf("Duplicate node name in RawFile nodes: %s", node.Name)
			klog.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
		}
		nodeNames[node.Name] = struct{}{}
		wanted = append(wanted, node.Name)
	}

	missing, err := nodeExistenceChecker(ctx, wanted)
	if err != nil {
		// Fail-open: log and accept. A transient API failure must not
		// block LSC edits; later reconciliation in the controller will
		// surface the misconfiguration via events.
		klog.Warningf("RawFile node existence check failed for LSC %q, accepting: %v", lsc.Name, err)
		return &kwhvalidating.ValidatorResult{Valid: true}, nil
	}
	if len(missing) > 0 {
		errMsg := fmt.Sprintf("RawFile nodes not found in the cluster: %s", strings.Join(missing, ", "))
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
	}

	return &kwhvalidating.ValidatorResult{Valid: true}, nil
}

func validateLVM(ctx context.Context, lsc *slv.LocalStorageClass) (*kwhvalidating.ValidatorResult, error) {
	cl, err := NewKubeClient("")
	if err != nil {
		klog.Fatal(err)
	}

	listDevice := &snc.LVMVolumeGroupList{}

	err = cl.List(ctx, listDevice)
	if err != nil {
		klog.Fatal(err)
	}

	errMsg := ""
	var lvmVolumeGroupUnique []string

	var thickNames, thinNames []string
	for _, lvmGroup := range lsc.Spec.LVM.LVMVolumeGroups {
		lvgExists := false

		if slices.Contains(lvmVolumeGroupUnique, lvmGroup.Name) {
			errMsg = fmt.Sprintf("There must be unique LVMVolumeGroup names (%s duplicates)", lvmGroup.Name)
			klog.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg},
				nil
		}

		lvmVolumeGroupUnique = append(lvmVolumeGroupUnique, lvmGroup.Name)

		for _, lvmVG := range listDevice.Items {
			if lvmVG.Name == lvmGroup.Name {
				lvgExists = true
				break
			}
		}

		if !lvgExists {
			errMsg = fmt.Sprintf("LVMVolumeGroup %s not found; ", lvmGroup.Name)
			klog.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg},
				nil
		}

		if lvmGroup.Thin == nil {
			thickNames = append(thickNames, lvmGroup.Name)
		} else {
			thinNames = append(thinNames, lvmGroup.Name)
		}
	}

	thinExists, thickExists := len(thinNames) > 0, len(thickNames) > 0

	if thinExists && lsc.Spec.LVM.Type == "Thick" {
		errMsg = fmt.Sprintf("There must be only thick pools with Thick LVM type. Found: %s.", strings.Join(thinNames, ", "))
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg},
			nil
	}

	if thickExists && lsc.Spec.LVM.Type == "Thin" {
		errMsg = fmt.Sprintf("There must be only thin pools with Thin LVM type. Found: %s.", strings.Join(thickNames, ", "))
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg},
			nil
	}

	if thickExists && thinExists {
		errMsg = "There must be only thin or thick pools simultaneously"
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
	}
	return &kwhvalidating.ValidatorResult{Valid: true}, nil
}
