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
	"log/slog"
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
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/monitoring"
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
	log     logger.Logger
	metrics monitoring.Recorder

	readyMu      sync.Mutex // protects ready
	ready        bool
	cl           client.Client
	storeManager utils.NodeStoreManager
	inFlight     *internal.InFlight

	csi.UnimplementedControllerServer
	csi.UnimplementedIdentityServer
	csi.UnimplementedNodeServer
}

// NewDriver returns a CSI plugin that contains the necessary gRPC
// interfaces to interact with Kubernetes over unix domain sockets for
// managing  disks
func NewDriver(csiAddress, driverName, address string, nodeName *string, log logger.Logger, metrics monitoring.Recorder, cl client.Client) (*Driver, error) {
	if driverName == "" {
		driverName = DefaultDriverName
	}

	st := utils.NewStore(log)

	return &Driver{
		name:              driverName,
		hostID:            *nodeName,
		csiAddress:        csiAddress,
		address:           address,
		log:               log,
		metrics:           metrics,
		waitActionTimeout: defaultWaitActionTimeout,
		cl:                cl,
		storeManager:      st,
		inFlight:          internal.NewInFlight(),
	}, nil
}

func (d *Driver) Run(ctx context.Context) error {
	log := d.log.Named("Run")

	u, err := url.Parse(d.csiAddress)
	if err != nil {
		return fmt.Errorf("unable to parse address: %q", err)
	}

	grpcAddr := path.Join(u.Host, filepath.FromSlash(u.Path))
	if u.Host == "" {
		grpcAddr = filepath.FromSlash(u.Path)
	}

	// These three values used to be dumped to stdout with fmt.Print, bypassing the
	// logger and the configured level entirely.
	log.Trace("resolved the listen addresses", "csiAddress", d.csiAddress, "parsedURL", u.String(), "grpcAddr", grpcAddr)

	// CSI plugins talk only over UNIX sockets currently
	if u.Scheme != "unix" {
		return fmt.Errorf("currently only unix domain sockets are supported, have: %s", u.Scheme)
	}
	// remove the socket if it's already there. This can happen if we
	// deploy a new version and the socket was created from the old running
	// plugin.
	log.Info("removing a stale socket", "grpcAddr", grpcAddr)
	if err := os.Remove(grpcAddr); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove unix domain socket file %s, error: %s", grpcAddr, err)
	}

	grpcListener, err := net.Listen(u.Scheme, grpcAddr)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}

	// log response errors and record the duration and status of every call.
	// Doing it in the interceptor covers each CSI method exactly once, including
	// the ones inherited from the Unimplemented*Server embeds.
	errHandler := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		d.metrics.ObserveOperation(info.FullMethod, start, err)
		if err != nil {
			log.Error("the CSI method failed", logger.Err(err), slog.String("method", info.FullMethod))
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
	log.Info("starting the servers", "grpcAddr", grpcAddr, "httpAddr", d.address)

	var eg errgroup.Group
	eg.Go(func() error {
		<-ctx.Done()
		return d.httpSrv.Shutdown(context.Background())
	})
	eg.Go(func() error {
		go func() {
			<-ctx.Done()
			log.Info("server stopped")
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

	return eg.Wait()
}
