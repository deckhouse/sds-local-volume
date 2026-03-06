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

	fmt.Print("d.csiAddress", d.csiAddress)
	fmt.Print("u", u)

	grpcAddr := path.Join(u.Host, filepath.FromSlash(u.Path))
	if u.Host == "" {
		grpcAddr = filepath.FromSlash(u.Path)
	}

	fmt.Print("grpcAddr", grpcAddr)

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
			d.log.Info("[RawFileCleanup] Stopping cleanup goroutine")
			return
		case <-ticker.C:
			d.processRawFilePVs(ctx)
		}
	}
}

// processRawFilePVs handles finalizer management and cleanup for RawFile PVs
func (d *Driver) processRawFilePVs(ctx context.Context) {
	d.log.Debug("[RawFileCleanup] Starting RawFile PV processing")

	// List all local volumes
	volumes, err := d.rawfileManager.ListVolumes()
	if err != nil {
		d.log.Warning(fmt.Sprintf("[RawFileCleanup] Failed to list volumes: %v", err))
		return
	}

	d.log.Debug(fmt.Sprintf("[RawFileCleanup] Found %d local volumes", len(volumes)))

	// Process each volume
	for _, volumeID := range volumes {
		pv, err := d.getPV(ctx, volumeID)
		if err != nil {
			// PV not found - skip, don't delete files without proper PV tracking
			d.log.Debug(fmt.Sprintf("[RawFileCleanup] PV %s not found, skipping", volumeID))
			continue
		}

		// Check if this is a RawFile volume (check VolumeAttributes)
		if pv.Spec.CSI == nil || pv.Spec.CSI.VolumeAttributes == nil {
			continue
		}
		volumeType := pv.Spec.CSI.VolumeAttributes[internal.TypeKey]
		if volumeType != internal.RawFile {
			continue
		}

		// Only add finalizer for PVs with Delete reclaim policy
		// For Retain policy, we don't manage file deletion
		if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
			d.log.Debug(fmt.Sprintf("[RawFileCleanup] PV %s has ReclaimPolicy %s, skipping finalizer", volumeID, pv.Spec.PersistentVolumeReclaimPolicy))
			continue
		}

		// Ensure our finalizer is present on the PV (only for Delete policy)
		if !d.hasFinalizer(pv) {
			d.log.Info(fmt.Sprintf("[RawFileCleanup] Adding finalizer to PV %s (ReclaimPolicy: Delete)", volumeID))
			if err := d.addFinalizer(ctx, pv); err != nil {
				d.log.Warning(fmt.Sprintf("[RawFileCleanup] Failed to add finalizer to PV %s: %v", volumeID, err))
				continue
			}
		}

		// Check if PV is being deleted (has DeletionTimestamp) and has our finalizer
		if pv.DeletionTimestamp != nil && d.hasFinalizer(pv) {
			d.log.Info(fmt.Sprintf("[RawFileCleanup] PV %s is being deleted, cleaning up volume", volumeID))

			// Detach loop device if attached
			volumePath := d.rawfileManager.GetVolumePath(volumeID)
			if err := d.rawfileManager.DetachLoopDevice(volumePath); err != nil {
				d.log.Warning(fmt.Sprintf("[RawFileCleanup] Failed to detach loop device for %s: %v", volumeID, err))
			}

			// Delete the volume file
			if err := d.rawfileManager.DeleteVolume(volumeID); err != nil {
				d.log.Error(err, fmt.Sprintf("[RawFileCleanup] Failed to delete volume %s", volumeID))
				continue // Don't remove finalizer if deletion failed
			}

			d.log.Info(fmt.Sprintf("[RawFileCleanup] Successfully deleted volume %s, removing finalizer", volumeID))

			// Remove finalizer from PV
			if err := d.removeFinalizer(ctx, pv); err != nil {
				d.log.Error(err, fmt.Sprintf("[RawFileCleanup] Failed to remove finalizer from PV %s", volumeID))
			}
		}
	}

	d.log.Debug("[RawFileCleanup] Processing completed")
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

// addFinalizer adds our RawFile finalizer to the PV
func (d *Driver) addFinalizer(ctx context.Context, pv *corev1.PersistentVolume) error {
	pv.Finalizers = append(pv.Finalizers, internal.RawFilePVFinalizer)
	return d.cl.Update(ctx, pv)
}

// removeFinalizer removes our RawFile finalizer from the PV
func (d *Driver) removeFinalizer(ctx context.Context, pv *corev1.PersistentVolume) error {
	// Re-fetch PV to get latest version
	freshPV, err := d.getPV(ctx, pv.Name)
	if err != nil {
		return fmt.Errorf("failed to get fresh PV: %w", err)
	}

	// Remove our finalizer
	var newFinalizers []string
	for _, f := range freshPV.Finalizers {
		if f != internal.RawFilePVFinalizer {
			newFinalizers = append(newFinalizers, f)
		}
	}
	freshPV.Finalizers = newFinalizers

	return d.cl.Update(ctx, freshPV)
}

// getPV retrieves a PersistentVolume by name
func (d *Driver) getPV(ctx context.Context, pvName string) (*corev1.PersistentVolume, error) {
	var pv corev1.PersistentVolume
	if err := d.cl.Get(ctx, client.ObjectKey{Name: pvName}, &pv); err != nil {
		return nil, err
	}
	return &pv, nil
}
