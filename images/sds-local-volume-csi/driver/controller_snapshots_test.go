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
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/deckhouse/sds-common-lib/logger"
	"github.com/deckhouse/sds-local-volume/lib/go/common/pkg/feature"
	"github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

// Runs under every edition tag. An edition that advertises a snapshot RPC it
// does not compile in fails every VolumeSnapshot with Unimplemented, and one
// that compiles it in without advertising it leaves csi-snapshotter idle.
func TestSnapshotCapabilitiesMatchImplementation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	d := &Driver{log: logger.NewNop(), cl: fake.NewClientBuilder().WithScheme(scheme).Build()}
	ctx := context.Background()

	resp, err := d.ControllerGetCapabilities(ctx, &csi.ControllerGetCapabilitiesRequest{})
	require.NoError(t, err)
	advertised := map[csi.ControllerServiceCapability_RPC_Type]bool{}
	for _, c := range resp.Capabilities {
		advertised[c.GetRpc().GetType()] = true
	}

	rpcs := []struct {
		name       string
		capability csi.ControllerServiceCapability_RPC_Type
		call       func() error
	}{
		{"CreateSnapshot", csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT, func() error {
			_, err := d.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "snap-a", SourceVolumeId: "pvc-missing"})
			return err
		}},
		{"DeleteSnapshot", csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT, func() error {
			_, err := d.DeleteSnapshot(ctx, &csi.DeleteSnapshotRequest{SnapshotId: "snap-missing"})
			return err
		}},
		{"ListSnapshots", csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS, func() error {
			_, err := d.ListSnapshots(ctx, &csi.ListSnapshotsRequest{})
			return err
		}},
	}

	for _, rpc := range rpcs {
		t.Run(rpc.name, func(t *testing.T) {
			implemented := status.Code(rpc.call()) != codes.Unimplemented
			assert.Equal(t, implemented, advertised[rpc.capability], "%s is advertised: %v, implemented: %v", rpc.capability, advertised[rpc.capability], implemented)
			assert.Equal(t, feature.SnapshotsEnabled(), implemented, "feature.SnapshotsEnabled() is %v, %s implemented: %v", feature.SnapshotsEnabled(), rpc.name, implemented)
		})
	}
}
