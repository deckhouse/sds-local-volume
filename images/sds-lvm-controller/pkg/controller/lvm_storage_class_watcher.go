package controller

import (
	"context"
	"fmt"
	"sds-lvm-controller/pkg/config"
	"sds-lvm-controller/pkg/logger"
	"sds-lvm-controller/pkg/monitoring"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	LVMStorageClassCtrlName = "lvm-storage-class-controller"
)

func RunLVMStorageClassWatcherController(
	ctx context.Context,
	mgr manager.Manager,
	cfg config.Options,
	log logger.Logger,
	metrics monitoring.Metrics,
) (controller.Controller, error) {
	// cl := mgr.GetClient()

	c, err := controller.New(LVMStorageClassCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		log.Error(err, "[RunLVMStorageClassWatcherController] unable to create controller")
		return nil, err
	}

	fmt.Println("Hello from " + LVMStorageClassCtrlName)

	return c, nil
}
