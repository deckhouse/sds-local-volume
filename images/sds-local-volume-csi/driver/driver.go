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
// 1. Processing PVs being deleted (with our finalizer) — delete file, remove finalizer
// 2. Cleaning up orphaned volume files (PV no longer exists)
func (d *Driver) processRawFilePVs(ctx context.Context) {
	d.log.Debug("[RawFileCleanup] Starting RawFile PV processing")

	volumes, err := d.rawfileManager.ListVolumes()
	if err != nil {
		d.log.Warning(fmt.Sprintf("[RawFileCleanup] Failed to list volumes: %v", err))
		return
	}

	d.log.Debug(fmt.Sprintf("[RawFileCleanup] Found %d local volumes", len(volumes)))

	for _, volumeID := range volumes {
		pv, err := utils.GetPersistentVolume(ctx, d.cl, volumeID)
		if err != nil {
			// PV not found — check if volume file is old enough to be considered orphaned.
			// Newly created files may not have a PV yet (API propagation delay).
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

		if pv.Spec.CSI == nil || pv.Spec.CSI.VolumeAttributes == nil {
			continue
		}
		if pv.Spec.CSI.VolumeAttributes[internal.TypeKey] != internal.RawFile {
			continue
		}

		// Process PVs being deleted that have our finalizer
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

// cleanupOrphanedVolume removes a volume file that has no corresponding PV
func (d *Driver) cleanupOrphanedVolume(volumeID string) {
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

// addFinalizer adds our RawFile finalizer to the PV using Patch with retry on conflict
func (d *Driver) addFinalizer(ctx context.Context, pvName string) error {
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		pv, err := utils.GetPersistentVolume(ctx, d.cl, pvName)
		if err != nil {
			return fmt.Errorf("failed to get PV: %w", err)
		}
		if d.hasFinalizer(pv) {
			return nil
		}
		patch := client.MergeFrom(pv.DeepCopy())
		pv.Finalizers = append(pv.Finalizers, internal.RawFilePVFinalizer)
		if err := d.cl.Patch(ctx, pv, patch); err != nil {
			if kerrors.IsConflict(err) && attempt < maxRetries-1 {
				continue
			}
			return fmt.Errorf("failed to add finalizer to PV %s: %w", pvName, err)
		}
		return nil
	}
	return fmt.Errorf("failed to add finalizer to PV %s after %d retries", pvName, maxRetries)
}

// removeFinalizer removes our RawFile finalizer from the PV using Patch with retry on conflict
func (d *Driver) removeFinalizer(ctx context.Context, pvName string) error {
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		pv, err := utils.GetPersistentVolume(ctx, d.cl, pvName)
		if err != nil {
			return fmt.Errorf("failed to get PV: %w", err)
		}
		if !d.hasFinalizer(pv) {
			return nil
		}
		patch := client.MergeFrom(pv.DeepCopy())
		var newFinalizers []string
		for _, f := range pv.Finalizers {
			if f != internal.RawFilePVFinalizer {
				newFinalizers = append(newFinalizers, f)
			}
		}
		pv.Finalizers = newFinalizers
		if err := d.cl.Patch(ctx, pv, patch); err != nil {
			if kerrors.IsConflict(err) && attempt < maxRetries-1 {
				continue
			}
			return fmt.Errorf("failed to remove finalizer from PV %s: %w", pvName, err)
		}
		return nil
	}
	return fmt.Errorf("failed to remove finalizer from PV %s after %d retries", pvName, maxRetries)
}

