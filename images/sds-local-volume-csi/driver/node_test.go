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
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/deckhouse/sds-common-lib/logger"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/utils"
)

type fakeNodeStore struct {
	utils.NodeStoreManager
	pathExists  bool
	resizeErr   error
	resizeCalls int
}

func (f *fakeNodeStore) PathExists(string) (bool, error) {
	return f.pathExists, nil
}

func (f *fakeNodeStore) ResizeFS(string) error {
	f.resizeCalls++
	return f.resizeErr
}

func TestNodeExpandVolume(t *testing.T) {
	blockCap := &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}}
	mountCap := &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}}

	cases := []struct {
		name        string
		capability  *csi.VolumeCapability
		store       *fakeNodeStore
		wantCode    codes.Code
		wantResizes int
	}{
		{"block device present", blockCap, &fakeNodeStore{pathExists: true}, codes.OK, 0},
		{"block device missing", blockCap, &fakeNodeStore{}, codes.NotFound, 0},
		{"filesystem resized", mountCap, &fakeNodeStore{}, codes.OK, 1},
		{"filesystem without capability resized", nil, &fakeNodeStore{}, codes.OK, 1},
		{"nothing mounted at the path", mountCap, &fakeNodeStore{resizeErr: fmt.Errorf("%w at /target", utils.ErrNotMounted)}, codes.NotFound, 1},
		{"resize fails", mountCap, &fakeNodeStore{resizeErr: errors.New("resize2fs failed")}, codes.Internal, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &Driver{log: logger.NewNop(), storeManager: c.store}
			_, err := d.NodeExpandVolume(context.Background(), &csi.NodeExpandVolumeRequest{
				VolumeId:         "pvc-1",
				VolumePath:       "/target",
				VolumeCapability: c.capability,
			})
			assert.Equal(t, c.wantCode, status.Code(err))
			assert.Equal(t, c.wantResizes, c.store.resizeCalls)
		})
	}
}
