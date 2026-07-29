//go:build ee

/*
Copyright 2025 Flant JSC
Licensed under the Deckhouse Platform Enterprise Edition (EE) license. See https://github.com/deckhouse/deckhouse/blob/main/ee/LICENSE
*/

package driver

import (
	"context"
	"log/slog"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/protobuf/ptypes/timestamp"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	kerrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/utils"
	"github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func (d *Driver) CreateSnapshot(ctx context.Context, request *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	traceID := uuid.New().String()

	log := d.log.Named("CreateSnapshot").With("traceID", traceID, "sourceVolumeID", request.SourceVolumeId)
	log.Trace("start", "request", request.String())

	llv, err := utils.GetLVMLogicalVolume(ctx, d.cl, request.SourceVolumeId, "")
	if err != nil {
		log.Error("unable to get the source LVMLogicalVolume", logger.Err(err))
		return nil, status.Errorf(codes.Internal, "error getting LVMLogicalVolume %s: %s", request.SourceVolumeId, err.Error())
	}

	if llv.Spec.Type != internal.LVMTypeThin {
		return nil, status.Errorf(codes.InvalidArgument, "Source LVMLogicalVolume '%s' is not of 'Thin' type", request.SourceVolumeId)
	}

	if llv.Status == nil || llv.Status.ActualSize.Value() == 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "Source LVMLogicalVolume '%s' ActualSize is unknown", request.SourceVolumeId)
	}

	lvg, err := utils.GetLVMVolumeGroup(ctx, d.cl, llv.Spec.LVMVolumeGroupName)
	if err != nil {
		log.Error("unable to get the LVMVolumeGroup", logger.Err(err), slog.String("lvgName", llv.Spec.LVMVolumeGroupName))
		return nil, status.Errorf(codes.Internal, "error getting LVMVolumeGroup %s: %s", llv.Spec.LVMVolumeGroupName, err.Error())
	}

	freeSpace, err := utils.GetLVMThinPoolFreeSpace(*lvg, llv.Spec.Thin.PoolName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get free space for thin pool %s in lvg %s: %v", llv.Spec.Thin.PoolName, lvg.Name, err)
	}

	if freeSpace.Value() < llv.Status.ActualSize.Value() {
		return nil, status.Errorf(
			codes.FailedPrecondition,
			"not enough space in pool %s (lvg %s): %s; need at least %s",
			llv.Spec.Thin.PoolName,
			lvg.Name,
			freeSpace.String(),
			llv.Status.ActualSize.String(),
		)
	}

	// the snapshots are required to be created in the same node and device class as the source volume.

	// suggested name is in form "{prefix}-{uuid}", where {prefix} is specified as external-snapshotter argument
	// {prefix} can not be the default "snapshot", since it's reserved keyword in LVM
	name := request.Name

	actualNameOnTheNode := request.Parameters[internal.ActualNameOnTheNodeKey]
	if actualNameOnTheNode == "" {
		actualNameOnTheNode = name
	}

	_, err = utils.CreateLVMLogicalVolumeSnapshot(
		ctx,
		d.cl,
		d.log,
		name,
		v1alpha1.LVMLogicalVolumeSnapshotSpec{
			ActualSnapshotNameOnTheNode: actualNameOnTheNode,
			LVMLogicalVolumeName:        llv.Name,
		},
	)
	if err != nil {
		if kerrors.IsAlreadyExists(err) {
			log.Info("the LVMLogicalVolumeSnapshot already exists, skipping creation", "llvsName", name)
		} else {
			log.Error("unable to create the LVMLogicalVolumeSnapshot", logger.Err(err), slog.String("llvsName", name))
			return nil, err
		}
	}

	attemptCounter, err := utils.WaitForLLVSStatusUpdate(ctx, d.cl, log, name)
	if err != nil {
		log.Error("the LVMLogicalVolumeSnapshot did not become ready, deleting it", logger.Err(err), slog.String("llvsName", request.Name))

		deleteErr := utils.DeleteLVMLogicalVolumeSnapshot(ctx, d.cl, log, request.Name)
		if deleteErr != nil {
			log.Error("unable to delete the LVMLogicalVolumeSnapshot after a failed wait", logger.Err(deleteErr), slog.String("llvsName", request.Name))
		}

		log.Error("unable to create the snapshot", logger.Err(err))
		return nil, err
	}
	log.Trace("the LVMLogicalVolumeSnapshot became ready", "attempts", attemptCounter)

	// Reported from the status, not from Spec.Size: the node rounds the LV up to the
	// extent boundary, and volumes provisioned before the CSI side started aligning
	// still carry an unaligned Spec.Size (e.g. 35Mi against a 36Mi LV). Restoring
	// such a snapshot would then ask for a size the source does not have. The
	// precondition at the top of this function already guarantees a non-zero
	// ActualSize, and the free-space check above is made against the same value.
	sizeBytes := llv.Status.ActualSize.Value()

	return &csi.CreateSnapshotResponse{
		Snapshot: &csi.Snapshot{
			SnapshotId:     name,
			SourceVolumeId: request.SourceVolumeId,
			SizeBytes:      sizeBytes,
			CreationTime: &timestamp.Timestamp{
				Seconds: time.Now().Unix(),
				Nanos:   0,
			},
			ReadyToUse: true,
		},
	}, nil
}

func (d *Driver) DeleteSnapshot(ctx context.Context, request *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	if len(request.SnapshotId) == 0 {
		return nil, status.Error(codes.InvalidArgument, "SnapshotId ID cannot be empty")
	}

	traceID := uuid.New().String()
	log := d.log.Named("DeleteSnapshot").With("traceID", traceID, "snapshotID", request.SnapshotId)
	log.Trace("start", "request", request.String())

	if err := utils.DeleteLVMLogicalVolumeSnapshot(ctx, d.cl, log, request.SnapshotId); err != nil {
		log.Error("unable to delete the LVMLogicalVolumeSnapshot", logger.Err(err))
	}

	log.Info("snapshot deleted successfully")

	return &csi.DeleteSnapshotResponse{}, nil
}

func (d *Driver) ListSnapshots(_ context.Context, _ *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	d.log.Named("ListSnapshots").Info("called")
	return nil, nil
}
