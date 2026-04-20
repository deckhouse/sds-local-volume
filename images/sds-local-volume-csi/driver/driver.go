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

package driver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/rawfile"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/utils"
)

const (
	// DefaultDriverName defines the name that is used in Kubernetes and the CSI
	// system for the canonical, official name of this plugin
	DefaultDriverName = "local.csi.storage.deckhouse.io"
	// DefaultAddress is the default address that the csi plugin will serve its
	// http handler on.
	DefaultAddress           = "127.0.0.1:12302"
	defaultWaitActionTimeout = 5 * time.Minute
)

var (
	version string
)

type Driver struct {
	name                  string
	publishInfoVolumeName string

	csiAddress        string
	address           string
	hostID            string
	waitActionTimeout time.Duration

	srv     *grpc.Server
	httpSrv http.Server
	log     *logger.Logger

	readyMu        sync.Mutex // protects ready
	ready          bool
	cl             client.Client
	storeManager   utils.NodeStoreManager
	rawfileManager *rawfile.Manager
	inFlight       *internal.InFlight

	csi.UnimplementedControllerServer
	csi.UnimplementedIdentityServer
	csi.UnimplementedNodeServer
}

// NewDriver returns a CSI plugin that contains the necessary gRPC
// interfaces to interact with Kubernetes over unix domain sockets for
// managing  disks
func NewDriver(csiAddress, driverName, address string, nodeName *string, log *logger.Logger, cl client.Client) (*Driver, error) {
	if driverName == "" {
		driverName = DefaultDriverName
	}

	st := utils.NewStore(log)

	// Initialize rawfile manager with default data directory
	rfm := rawfile.NewManager(log, internal.GetRawFileDataDir())
	if err := rfm.EnsureDataDir(); err != nil {
		log.Warning(fmt.Sprintf("Failed to ensure rawfile data directory: %v", err))
	}

	return &Driver{
		name:              driverName,
		hostID:            *nodeName,
		csiAddress:        csiAddress,
		address:           address,
		log:               log,
		waitActionTimeout: defaultWaitActionTimeout,
		cl:                cl,
		storeManager:      st,
		rawfileManager:    rfm,
		inFlight:          internal.NewInFlight(),
	}, nil
}

func (d *Driver) Run(ctx context.Context) error {
	u, err := url.Parse(d.csiAddress)
	if err != nil {
		return fmt.Errorf("unable to parse address: %q", err)
	}

	grpcAddr := path.Join(u.Host, filepath.FromSlash(u.Path))
	if u.Host == "" {
		grpcAddr = filepath.FromSlash(u.Path)
	}

	// CSI plugins talk only over UNIX sockets currently
	if u.Scheme != "unix" {
		return fmt.Errorf("currently only unix domain sockets are supported, have: %s", u.Scheme)
	}
	// remove the socket if it's already there. This can happen if we
	// deploy a new version and the socket was created from the old running
	// plugin.
	d.log.Info(fmt.Sprintf("socket %s removing socket", grpcAddr))
	if err := os.Remove(grpcAddr); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove unix domain socket file %s, error: %s", grpcAddr, err)
	}

	grpcListener, err := net.Listen(u.Scheme, grpcAddr)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}

	// log response errors for better observability
	errHandler := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		resp, err := handler(ctx, req)
		if err != nil {
			d.log.Error(err, fmt.Sprintf("method %s method failed ", info.FullMethod))
		}
		return resp, err
	}

	d.srv = grpc.NewServer(grpc.UnaryInterceptor(errHandler))
	csi.RegisterIdentityServer(d.srv, d)
	csi.RegisterControllerServer(d.srv, d)
	csi.RegisterNodeServer(d.srv, d)

	httpListener, err := net.Listen("tcp", d.address)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	d.httpSrv = http.Server{
		Handler: mux,
	}

	d.ready = true
	d.log.Info(fmt.Sprintf("grpc_addr %s http_addr %s starting server", grpcAddr, d.address))

	var eg errgroup.Group
	eg.Go(func() error {
		<-ctx.Done()
		return d.httpSrv.Shutdown(context.Background())
	})
	eg.Go(func() error {
		go func() {
			<-ctx.Done()
			d.log.Info("server stopped")
			d.readyMu.Lock()
			d.ready = false
			d.readyMu.Unlock()
			d.srv.GracefulStop()
		}()
		return d.srv.Serve(grpcListener)
	})
	eg.Go(func() error {
		err := d.httpSrv.Serve(httpListener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	// Start RawFile cleanup goroutine
	eg.Go(func() error {
		d.runRawFileCleanup(ctx)
		return nil
	})

	return eg.Wait()
}

// runRawFileCleanup periodically checks for RawFile volumes that need cleanup
func (d *Driver) runRawFileCleanup(ctx context.Context) {
	// Initial delay before first cleanup
	initialDelay := 30 * time.Second
	cleanupInterval := 1 * time.Minute

	d.log.Info(fmt.Sprintf("[RawFileCleanup] Starting cleanup goroutine, initial delay: %v, interval: %v", initialDelay, cleanupInterval))

	// Wait for initial delay
	select {
	case <-ctx.Done():
		return
	case <-time.After(initialDelay):
	}

	// Run first cleanup immediately after delay
	d.processRawFilePVs(ctx)

	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.log.Debug("[RawFileCleanup] Stopping cleanup goroutine")
			return
		case <-ticker.C:
			d.processRawFilePVs(ctx)
		}
	}
}

// orphanGracePeriod is the minimum age a volume file must have before it can
// be considered orphaned. This prevents deleting files that were just created
// but whose PV hasn't appeared in the API yet.
const orphanGracePeriod = 5 * time.Minute

// processRawFilePVs handles cleanup for RawFile PVs.
// Finalizers are added at volume creation time (NodeStageVolume).
// This goroutine handles:
//  1. Processing PVs being deleted (with our finalizer) — delete file, remove finalizer
//  2. Cleaning up orphaned volume files (PV no longer exists)
//
// To avoid an O(N) burst of GET PV calls on every cycle (one per local file),
// the apiserver is queried exactly once per cycle via LIST and the result is
// indexed by PV name (PV name == CSI volumeID). This drops apiserver pressure
// from O(N) to O(1) per cycle and keeps memory bounded by the cluster's PV
// count rather than by local volume count.
func (d *Driver) processRawFilePVs(ctx context.Context) {
	d.log.Debug("[RawFileCleanup] Starting RawFile PV processing")

	volumes, err := d.rawfileManager.ListVolumes()
	if err != nil {
		d.log.Warning(fmt.Sprintf("[RawFileCleanup] Failed to list volumes: %v", err))
		return
	}
	d.log.Debug(fmt.Sprintf("[RawFileCleanup] Found %d local volumes", len(volumes)))

	pvIndex, err := d.listRawFilePVs(ctx)
	if err != nil {
		d.log.Warning(fmt.Sprintf("[RawFileCleanup] Failed to list PVs, skipping cycle: %v", err))
		return
	}
	d.log.Debug(fmt.Sprintf("[RawFileCleanup] Indexed %d RawFile PVs from apiserver", len(pvIndex)))

	for _, volumeID := range volumes {
		pv, found := pvIndex[volumeID]
		if !found {
			// No PV with this name. Either truly orphaned, or this is not
			// a RawFile PV (other types are intentionally not in pvIndex).
			// Use modTime grace period to avoid racing freshly-created files
			// whose PV hasn't yet been observed by our cache.
			volInfo, infoErr := d.rawfileManager.GetVolumeInfo(volumeID)
			if infoErr != nil {
				d.log.Warning(fmt.Sprintf("[RawFileCleanup] PV %s not found and failed to get volume info: %v", volumeID, infoErr))
				continue
			}
			age := time.Since(volInfo.ModTime)
			if age < orphanGracePeriod {
				d.log.Debug(fmt.Sprintf("[RawFileCleanup] PV %s not found but volume is only %v old (grace period %v), skipping", volumeID, age.Round(time.Second), orphanGracePeriod))
				continue
			}
			d.log.Info(fmt.Sprintf("[RawFileCleanup] PV %s not found and volume is %v old, cleaning up orphaned volume file", volumeID, age.Round(time.Second)))
			d.cleanupOrphanedVolume(volumeID)
			continue
		}

		// Process PVs being deleted that have our finalizer.
		if pv.DeletionTimestamp != nil && d.hasFinalizer(pv) {
			d.log.Info(fmt.Sprintf("[RawFileCleanup] PV %s is being deleted, cleaning up volume", volumeID))

			volumePath := d.rawfileManager.GetVolumePath(volumeID)
			if err := d.rawfileManager.DetachLoopDevice(volumePath); err != nil {
				d.log.Warning(fmt.Sprintf("[RawFileCleanup] Failed to detach loop device for %s: %v", volumeID, err))
			}

			if err := d.rawfileManager.DeleteVolume(volumeID); err != nil {
				d.log.Error(err, fmt.Sprintf("[RawFileCleanup] Failed to delete volume %s", volumeID))
				continue
			}

			d.log.Info(fmt.Sprintf("[RawFileCleanup] Successfully deleted volume %s, removing finalizer", volumeID))

			if err := d.removeFinalizer(ctx, volumeID); err != nil {
				d.log.Error(err, fmt.Sprintf("[RawFileCleanup] Failed to remove finalizer from PV %s", volumeID))
			}
		}
	}

	d.log.Debug("[RawFileCleanup] Processing completed")
}

// listRawFilePVs returns a map[pvName]*PV containing only PVs provisioned by
// this driver and tagged as RawFile. Non-RawFile PVs are filtered out so the
// caller can rely on map presence as "this volumeID corresponds to a live
// RawFile PV". A single LIST is issued per call.
func (d *Driver) listRawFilePVs(ctx context.Context) (map[string]*corev1.PersistentVolume, error) {
	pvList := &corev1.PersistentVolumeList{}
	if err := d.cl.List(ctx, pvList); err != nil {
		return nil, fmt.Errorf("list PVs: %w", err)
	}
	out := make(map[string]*corev1.PersistentVolume, len(pvList.Items))
	for i := range pvList.Items {
		pv := &pvList.Items[i]
		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != DefaultDriverName {
			continue
		}
		if pv.Spec.CSI.VolumeAttributes[internal.TypeKey] != internal.RawFile {
			continue
		}
		out[pv.Name] = pv
	}
	return out, nil
}

// cleanupOrphanedVolume removes a volume file that has no corresponding PV.
// To honor PersistentVolumeReclaimPolicy: Retain even after the PV API object is
// gone, we read the on-disk reclaim marker (written by NodeStageVolume) and
// only delete files whose persisted policy is "Delete". An empty / missing
// marker is treated as the conservative default — keep the file and log.
func (d *Driver) cleanupOrphanedVolume(volumeID string) {
	policy, err := d.rawfileManager.GetReclaimPolicy(volumeID)
	if err != nil {
		d.log.Warning(fmt.Sprintf("[RawFileCleanup] Failed to read reclaim marker for orphaned volume %s, keeping file: %v", volumeID, err))
		return
	}
	if !strings.EqualFold(policy, string(corev1.PersistentVolumeReclaimDelete)) {
		d.log.Info(fmt.Sprintf("[RawFileCleanup] Orphaned volume %s has ReclaimPolicy=%q (default Retain when empty), keeping file on disk", volumeID, policy))
		return
	}

	volumePath := d.rawfileManager.GetVolumePath(volumeID)
	if err := d.rawfileManager.DetachLoopDevice(volumePath); err != nil {
		d.log.Warning(fmt.Sprintf("[RawFileCleanup] Failed to detach loop device for orphaned volume %s: %v", volumeID, err))
	}
	if err := d.rawfileManager.DeleteVolume(volumeID); err != nil {
		d.log.Error(err, fmt.Sprintf("[RawFileCleanup] Failed to delete orphaned volume %s", volumeID))
	} else {
		d.log.Info(fmt.Sprintf("[RawFileCleanup] Orphaned volume %s cleaned up", volumeID))
	}
}

// hasFinalizer checks if the PV has our RawFile finalizer
func (d *Driver) hasFinalizer(pv *corev1.PersistentVolume) bool {
	for _, f := range pv.Finalizers {
		if f == internal.RawFilePVFinalizer {
			return true
		}
	}
	return false
}

// finalizerMaxAttempts caps total tries (initial + retries) for the
// finalizer add/remove loops. Backoff is exponential between attempts.
const finalizerMaxAttempts = 5

// finalizerInitialBackoff is the initial sleep before retry #1; each
// subsequent retry doubles, capped by finalizerMaxBackoff.
const finalizerInitialBackoff = 100 * time.Millisecond

// finalizerMaxBackoff caps the per-retry sleep so a long flap of API errors
// does not stall NodeStageVolume / cleanup loops indefinitely.
const finalizerMaxBackoff = 2 * time.Second

// isRetryableAPIError reports whether err looks like a transient apiserver
// failure that is worth retrying. We treat 409 Conflict, 408/504 Server
// Timeouts, 429 TooManyRequests, 500 InternalError and 503
// ServiceUnavailable as retryable; anything else (NotFound, Forbidden,
// Invalid, …) propagates immediately.
func isRetryableAPIError(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case kerrors.IsConflict(err),
		kerrors.IsServerTimeout(err),
		kerrors.IsTimeout(err),
		kerrors.IsTooManyRequests(err),
		kerrors.IsInternalError(err),
		kerrors.IsServiceUnavailable(err):
		return true
	}
	return false
}

// patchFinalizerWithRetry runs mutate(pv) (which must return early when
// no patch is needed), then PATCHes the PV. The whole get/mutate/patch
// cycle is retried with exponential backoff for transient apiserver
// errors; non-retryable errors are returned immediately.
func (d *Driver) patchFinalizerWithRetry(ctx context.Context, pvName, op string, mutate func(*corev1.PersistentVolume) bool) error {
	backoff := finalizerInitialBackoff
	for attempt := 1; attempt <= finalizerMaxAttempts; attempt++ {
		pv, err := utils.GetPersistentVolume(ctx, d.cl, pvName)
		if err != nil {
			if isRetryableAPIError(err) && attempt < finalizerMaxAttempts {
				if waitErr := sleepCtx(ctx, backoff); waitErr != nil {
					return waitErr
				}
				backoff = nextBackoff(backoff)
				continue
			}
			return fmt.Errorf("failed to get PV %s: %w", pvName, err)
		}
		patch := client.MergeFrom(pv.DeepCopy())
		if !mutate(pv) {
			return nil
		}
		if err := d.cl.Patch(ctx, pv, patch); err != nil {
			if isRetryableAPIError(err) && attempt < finalizerMaxAttempts {
				if waitErr := sleepCtx(ctx, backoff); waitErr != nil {
					return waitErr
				}
				backoff = nextBackoff(backoff)
				continue
			}
			return fmt.Errorf("failed to %s finalizer on PV %s: %w", op, pvName, err)
		}
		return nil
	}
	return fmt.Errorf("failed to %s finalizer on PV %s after %d attempts", op, pvName, finalizerMaxAttempts)
}

// sleepCtx blocks for d or until ctx is cancelled, whichever happens first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > finalizerMaxBackoff {
		return finalizerMaxBackoff
	}
	return next
}

// addFinalizer adds our RawFile finalizer to the PV.
func (d *Driver) addFinalizer(ctx context.Context, pvName string) error {
	return d.patchFinalizerWithRetry(ctx, pvName, "add", func(pv *corev1.PersistentVolume) bool {
		if d.hasFinalizer(pv) {
			return false
		}
		pv.Finalizers = append(pv.Finalizers, internal.RawFilePVFinalizer)
		return true
	})
}

// removeFinalizer removes our RawFile finalizer from the PV.
func (d *Driver) removeFinalizer(ctx context.Context, pvName string) error {
	return d.patchFinalizerWithRetry(ctx, pvName, "remove", func(pv *corev1.PersistentVolume) bool {
		if !d.hasFinalizer(pv) {
			return false
		}
		newFinalizers := pv.Finalizers[:0]
		for _, f := range pv.Finalizers {
			if f != internal.RawFilePVFinalizer {
				newFinalizers = append(newFinalizers, f)
			}
		}
		pv.Finalizers = newFinalizers
		return true
	})
}
