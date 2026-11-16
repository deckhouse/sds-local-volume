/*
Copyright 2026 Flant JSC

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

package driver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	kerrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/utils"
)

const (
	nodeConfiguratorFinalizer = "storage.deckhouse.io/sds-node-configurator"
	phaseCleaning             = "Cleaning"
	maxPendingReasonLen       = 1024
)

func deleteAPIError(err error) error {
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	code := codes.Internal
	if kerrors.IsConflict(err) {
		return status.Error(codes.Aborted, err.Error())
	}
	if kerrors.IsServerTimeout(err) || kerrors.IsTimeout(err) || kerrors.IsServiceUnavailable(err) ||
		kerrors.IsTooManyRequests(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		code = codes.Unavailable
	}
	return status.Error(code, err.Error())
}

func deletionPendingMessage(kind, name, phase, reason string, finalizers []string) string {
	foreign := slices.DeleteFunc(slices.Clone(finalizers), func(f string) bool {
		return f == utils.SDSLocalVolumeCSIFinalizer || f == nodeConfiguratorFinalizer
	})

	var b strings.Builder
	switch {
	case phase == phaseCleaning:
		fmt.Fprintf(&b, "volume cleanup in progress for %s %s: phase=%s reason=%q", kind, name, phase, truncate(reason))
	case phase == "" && reason == "":
		fmt.Fprintf(&b, "%s %s is still being deleted: waiting for the sds-node-configurator agent to report a status", kind, name)
	default:
		fmt.Fprintf(&b, "%s %s is still being deleted: phase=%s reason=%q", kind, name, phase, truncate(reason))
	}
	if len(foreign) > 0 {
		fmt.Fprintf(&b, "; blocked by finalizers %v", foreign)
	}
	return b.String()
}

func truncate(s string) string {
	if len(s) <= maxPendingReasonLen {
		return s
	}
	return strings.ToValidUTF8(s[:maxPendingReasonLen], "") + "..."
}
