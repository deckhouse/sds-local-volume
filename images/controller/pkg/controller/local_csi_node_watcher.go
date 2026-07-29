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

package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/yaml"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/config"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/monitoring"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const (
	LocalCSINodeWatcherCtrl = "local-csi-node-watcher-controller"

	localCsiNodeSelectorLabel    = "storage.deckhouse.io/sds-local-volume-node"
	nodeManualEvictionLabel      = "storage.deckhouse.io/sds-local-volume-need-manual-eviction"
	candidateManualEvictionLabel = "storage.deckhouse.io/sds-local-volume-candidate-for-eviction"
)

func RunLocalCSINodeWatcherController(
	mgr manager.Manager,
	cfg config.Options,
	log logger.Logger,
	metrics monitoring.Recorder,
) (controller.Controller, error) {
	cl := mgr.GetClient()
	log = log.Named(LocalCSINodeWatcherCtrl)

	c, err := controller.New(LocalCSINodeWatcherCtrl, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (res reconcile.Result, err error) {
			defer metrics.ObserveReconcile(LocalCSINodeWatcherCtrl, time.Now(), &res, &err)

			log.Info("start")
			if request.Name == cfg.ConfigSecretName {
				log.Debug("the request matches the config secret, reconciling", "configSecretName", cfg.ConfigSecretName)

				log.Debug("getting the secret")
				secret, err := getSecret(ctx, cl, request.Namespace, request.Name)
				if err != nil {
					log.Error("unable to get the secret", logger.Err(err))
					return reconcile.Result{}, err
				}
				log.Debug("got the secret")

				log.Debug("reconciling the local CSI nodes")
				err = reconcileLocalCSINodes(ctx, cl, log, secret)
				if err != nil {
					log.Error("unable to reconcile the local CSI nodes", logger.Err(err))
					return reconcile.Result{}, err
				}
				log.Debug("reconciled the local CSI nodes")

				return reconcile.Result{
					RequeueAfter: cfg.RequeueSecretInterval * time.Second,
				}, nil
			}

			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		return nil, err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &v1.Secret{}, &handler.TypedEnqueueRequestForObject[*v1.Secret]{}))

	return c, err
}

func getSecret(ctx context.Context, cl client.Client, namespace, name string) (*v1.Secret, error) {
	secret := &v1.Secret{}
	err := cl.Get(ctx,
		client.ObjectKey{
			Namespace: namespace,
			Name:      name,
		}, secret)
	return secret, err
}

func reconcileLocalCSINodes(ctx context.Context, cl client.Client, log logger.Logger, secret *v1.Secret) error {
	log.Debug("reading the node selector from the config")
	nodeSelector, err := getNodeSelectorFromConfig(secret)
	if err != nil {
		log.Error("unable to read the node selector from the secret", logger.Err(err), slog.String("secretNamespace", secret.Namespace), slog.String("secretName", secret.Name))
		return err
	}
	log.Trace("read the node selector", "nodeSelector", nodeSelector)

	log.Debug("listing the nodes matching the selector", "nodeSelector", nodeSelector)
	nodesWithSelector, err := getKubernetesNodesBySelector(ctx, cl, nodeSelector)
	if err != nil {
		log.Error("unable to list the nodes matching the selector", logger.Err(err), slog.String("nodeSelector", fmt.Sprintf("%v", nodeSelector)))
		return err
	}
	for _, n := range nodesWithSelector.Items {
		log.Trace("node matched the selector", "nodeName", n.Name)
	}

	labelNodesWithLocalCSIIfNeeded(ctx, cl, log, nodesWithSelector)
	log.Debug("labelled the selected nodes", "label", localCsiNodeSelectorLabel)

	log.Debug("clearing the nodes that no longer match the selector")
	log.Debug("listing all nodes")
	nodes, err := getKubeNodes(ctx, cl)
	if err != nil {
		log.Error("unable to list all nodes", logger.Err(err))
		return err
	}
	for _, n := range nodes.Items {
		log.Trace("listed a node", "nodeName", n.Name)
	}

	reconcileLocalCSILabels(ctx, cl, log, nodes, nodeSelector)
	log.Debug("removed the label from the nodes that no longer match the selector", "label", localCsiNodeSelectorLabel)

	return nil
}

func reconcileLocalCSILabels(ctx context.Context, cl client.Client, log logger.Logger, nodes *v1.NodeList, selector map[string]string) {
	var err error
	for _, node := range nodes.Items {
		log.Debug("reconciling a node", "nodeName", node.Name)
		if labels.Set(selector).AsSelector().Matches(labels.Set(node.Labels)) {
			log.Debug("the node still matches the selector, keeping the label", "label", localCsiNodeSelectorLabel, "nodeName", node.Name)

			err = clearManualEvictionLabelsIfNeeded(ctx, cl, log, node)
			if err != nil {
				log.Error("unable to remove the manual eviction labels", logger.Err(err), slog.String("nodeLabel", nodeManualEvictionLabel), slog.String("candidateLabel", candidateManualEvictionLabel))
			}
			continue
		}

		if _, exist := node.Labels[localCsiNodeSelectorLabel]; !exist {
			log.Debug("the node does not carry the label, nothing to remove", "label", localCsiNodeSelectorLabel, "nodeName", node.Name)
			continue
		}

		lvgsForManualEviction, lscsForManualEviction, err := getManuallyEvictedLVGsAndLSCs(ctx, cl, node)
		if err != nil {
			log.Error("unable to get the LVMVolumeGroups for manual eviction", logger.Err(err), slog.String("nodeName", node.Name))
			continue
		}

		if len(lvgsForManualEviction) == 0 &&
			len(lscsForManualEviction) == 0 {
			log.Debug("no dependent resources, removing the label from the node", "nodeName", node.Name, "label", localCsiNodeSelectorLabel)

			delete(node.Labels, localCsiNodeSelectorLabel)
			delete(node.Labels, nodeManualEvictionLabel)

			err = cl.Update(ctx, &node)
			if err != nil {
				log.Error("unable to update the node", logger.Err(err), slog.String("nodeName", node.Name))
				continue
			}

			log.Debug("removed the label from the node", "label", localCsiNodeSelectorLabel, "nodeName", node.Name)
		} else {
			lvgsNames := strings.Builder{}
			for _, lvg := range lvgsForManualEviction {
				lvgsNames.WriteString(fmt.Sprintf("%s ", lvg.Name))
			}
			lscNames := strings.Builder{}
			for _, lsc := range lscsForManualEviction {
				lscNames.WriteString(fmt.Sprintf("%s ", lsc.Name))
			}
			log.Warn("cannot remove the label: the node LVMVolumeGroups are still used by LocalStorageClasses", "label", localCsiNodeSelectorLabel, "nodeName", node.Name, "lvmVolumeGroups", lvgsNames.String(), "localStorageClasses", lscNames.String())

			added, err := addLabelOnTheNodeIfNotExist(ctx, cl, node, nodeManualEvictionLabel)
			if err != nil {
				log.Error("unable to update the node", logger.Err(err), slog.String("nodeName", node.Name))
				continue
			}
			if !added {
				log.Debug("the node already carries the manual eviction label", "label", nodeManualEvictionLabel, "nodeName", node.Name)
			} else {
				log.Debug("added the manual eviction label to the node", "label", nodeManualEvictionLabel, "nodeName", node.Name)
			}

			for _, lvg := range lvgsForManualEviction {
				added, err = addLabelOnTheLVGIfNotExist(ctx, cl, lvg, candidateManualEvictionLabel)
				if err != nil {
					log.Error("unable to update the LVMVolumeGroup", logger.Err(err), slog.String("lvgName", lvg.Name))
					continue
				}
				if !added {
					log.Debug("the LVMVolumeGroup already carries the candidate label", "label", candidateManualEvictionLabel, "lvgName", lvg.Name)
				} else {
					log.Debug("added the candidate label to the LVMVolumeGroup", "label", candidateManualEvictionLabel, "lvgName", lvg.Name)
				}
			}

			for _, lsc := range lscsForManualEviction {
				added, err = addLabelOnTheLSCIfNotExist(ctx, cl, lsc, candidateManualEvictionLabel)
				if err != nil {
					log.Error("unable to update the LocalStorageClass", logger.Err(err), slog.String("localStorageClass", lsc.Name))
					continue
				}
				if !added {
					log.Debug("the LocalStorageClass already carries the candidate label", "label", candidateManualEvictionLabel, "localStorageClass", lsc.Name)
				} else {
					log.Debug("added the candidate label to the LocalStorageClass", "label", candidateManualEvictionLabel, "localStorageClass", lsc.Name)
				}
			}
		}
		log.Debug("reconciled a node", "nodeName", node.Name)
	}
}

func addLabelOnTheLSCIfNotExist(ctx context.Context, cl client.Client, lsc slv.LocalStorageClass, label string) (bool, error) {
	if _, exist := lsc.Labels[label]; exist {
		return false, nil
	}

	if lsc.Labels == nil {
		lsc.Labels = make(map[string]string, 1)
	}

	lsc.Labels[label] = ""
	err := cl.Update(ctx, &lsc)
	if err != nil {
		return false, err
	}

	return true, nil
}

func addLabelOnTheLVGIfNotExist(ctx context.Context, cl client.Client, lvg snc.LVMVolumeGroup, label string) (bool, error) {
	if _, exist := lvg.Labels[label]; exist {
		return false, nil
	}

	if lvg.Labels == nil {
		lvg.Labels = make(map[string]string, 1)
	}
	lvg.Labels[label] = ""
	err := cl.Update(ctx, &lvg)
	if err != nil {
		return false, err
	}

	return true, nil
}

func addLabelOnTheNodeIfNotExist(ctx context.Context, cl client.Client, node v1.Node, label string) (bool, error) {
	if _, exist := node.Labels[label]; exist {
		return false, nil
	}

	node.Labels[label] = ""
	err := cl.Update(ctx, &node)
	if err != nil {
		return false, err
	}

	return true, nil
}

func clearManualEvictionLabelsIfNeeded(ctx context.Context, cl client.Client, log logger.Logger, node v1.Node) error {
	if _, exist := node.Labels[nodeManualEvictionLabel]; !exist {
		log.Debug("the node does not carry the label", "nodeName", node.Name, "label", nodeManualEvictionLabel)
	} else {
		log.Debug("removing the label from the node", "nodeName", node.Name, "label", nodeManualEvictionLabel)
		delete(node.Labels, nodeManualEvictionLabel)
		err := cl.Update(ctx, &node)
		if err != nil {
			log.Error("unable to update the node", logger.Err(err), slog.String("nodeName", node.Name))
			return err
		}
	}

	lvgList, err := getLVMVolumeGroups(ctx, cl)
	if err != nil {
		log.Error("unable to list the LVMVolumeGroups", logger.Err(err))
		return err
	}

	lvgs := make(map[string]snc.LVMVolumeGroup, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		lvgs[lvg.Name] = lvg
	}

	usedLvgs := make(map[string]snc.LVMVolumeGroup, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		for _, n := range lvg.Status.Nodes {
			if n.Name == node.Name {
				usedLvgs[lvg.Name] = lvg
			}
		}
	}

	log.Debug("removing the candidate label from the node LVMVolumeGroups", "label", candidateManualEvictionLabel, "nodeName", node.Name)
	for _, lvg := range usedLvgs {
		if _, exist := lvg.Labels[candidateManualEvictionLabel]; !exist {
			log.Debug("the LVMVolumeGroup does not carry the label", "lvgName", lvg.Name, "label", candidateManualEvictionLabel)
			continue
		}

		log.Debug("removing the label from the LVMVolumeGroup", "lvgName", lvg.Name, "label", candidateManualEvictionLabel)
		delete(lvg.Labels, candidateManualEvictionLabel)
		err = cl.Update(ctx, &lvg)
		if err != nil {
			log.Error("unable to update the LVMVolumeGroup", logger.Err(err), slog.String("lvgName", lvg.Name))
			continue
		}
		log.Debug("removed the label from the LVMVolumeGroup", "lvgName", lvg.Name, "label", candidateManualEvictionLabel)
	}
	log.Debug("removed the candidate label from the node LVMVolumeGroups", "label", candidateManualEvictionLabel, "nodeName", node.Name)

	lscList, err := getLocalStorageClasses(ctx, cl)
	if err != nil {
		return err
	}

	for _, lsc := range lscList.Items {
		if _, exist := lsc.Labels[candidateManualEvictionLabel]; !exist {
			log.Debug("the LocalStorageClass does not carry the label", "localStorageClass", lsc.Name, "label", candidateManualEvictionLabel)
			continue
		}

		healthy := true
		badLVGs := strings.Builder{}
		for _, lvg := range lsc.Spec.LVM.LVMVolumeGroups {
			kubeLvg := lvgs[lvg.Name]

			if _, exist := kubeLvg.Labels[candidateManualEvictionLabel]; exist {
				healthy = false
				badLVGs.WriteString(fmt.Sprintf("%s ", lvg.Name))
			}
		}

		if !healthy {
			log.Debug("the LocalStorageClass still has manually evicted LVMVolumeGroups, keeping the label", "lvmVolumeGroups", badLVGs.String(), "label", candidateManualEvictionLabel)
		} else {
			log.Debug("the LocalStorageClass has no manually evicted LVMVolumeGroup, removing the label", "label", candidateManualEvictionLabel)

			delete(lsc.Labels, candidateManualEvictionLabel)
			err = cl.Update(ctx, &lsc)
			if err != nil {
				log.Error("unable to update the LocalStorageClass", logger.Err(err), slog.String("localStorageClass", lsc.Name))
				continue
			}
			log.Debug("removed the label from the LocalStorageClass", "label", candidateManualEvictionLabel, "localStorageClass", lsc.Name)
		}
	}

	return nil
}

func getManuallyEvictedLVGsAndLSCs(ctx context.Context, cl client.Client, node v1.Node) (map[string]snc.LVMVolumeGroup, map[string]slv.LocalStorageClass, error) {
	lvgList, err := getLVMVolumeGroups(ctx, cl)
	if err != nil {
		return nil, nil, err
	}

	usedLvgs := make(map[string]snc.LVMVolumeGroup, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		for _, n := range lvg.Status.Nodes {
			if n.Name == node.Name {
				usedLvgs[lvg.Name] = lvg
			}
		}
	}

	lscList, err := getLocalStorageClasses(ctx, cl)
	if err != nil {
		return nil, nil, err
	}

	unhealthyLscs := make(map[string]slv.LocalStorageClass, len(lscList.Items))
	unhealthyLvgs := make(map[string]snc.LVMVolumeGroup, len(usedLvgs))

	// This case is a base case, when the controller did not label any resource.
	for _, lsc := range lscList.Items {
		for _, lvg := range lsc.Spec.LVM.LVMVolumeGroups {
			if _, match := usedLvgs[lvg.Name]; match {
				unhealthyLvgs[lvg.Name] = usedLvgs[lvg.Name]
				unhealthyLscs[lsc.Name] = lsc
			}
		}
	}

	// This case is needed to prevent ignoring unhealthy LVMVolumeGroup resources, if LocalStorageClasses were deleted.
	for _, lvg := range usedLvgs {
		if _, exist := lvg.Labels[candidateManualEvictionLabel]; exist {
			unhealthyLvgs[lvg.Name] = lvg
		}
	}

	return unhealthyLvgs, unhealthyLscs, nil
}

func getLVMVolumeGroups(ctx context.Context, cl client.Client) (*snc.LVMVolumeGroupList, error) {
	lvgList := &snc.LVMVolumeGroupList{}
	err := cl.List(ctx, lvgList)

	return lvgList, err
}

func getLocalStorageClasses(ctx context.Context, cl client.Client) (*slv.LocalStorageClassList, error) {
	lscList := &slv.LocalStorageClassList{}
	err := cl.List(ctx, lscList)
	return lscList, err
}

func getKubeNodes(ctx context.Context, cl client.Client) (*v1.NodeList, error) {
	nodes := &v1.NodeList{}
	err := cl.List(ctx, nodes)
	return nodes, err
}

func getNodeSelectorFromConfig(secret *v1.Secret) (map[string]string, error) {
	var sdsConfig config.SdsLocalVolumeConfig
	err := yaml.Unmarshal(secret.Data["config"], &sdsConfig)
	if err != nil {
		return nil, err
	}

	return sdsConfig.NodeSelector, nil
}

func getKubernetesNodesBySelector(ctx context.Context, cl client.Client, selector map[string]string) (*v1.NodeList, error) {
	nodes := &v1.NodeList{}
	err := cl.List(ctx, nodes, client.MatchingLabels(selector))
	return nodes, err
}

func labelNodesWithLocalCSIIfNeeded(ctx context.Context, cl client.Client, log logger.Logger, nodes *v1.NodeList) {
	var err error
	for _, node := range nodes.Items {
		if _, exist := node.Labels[localCsiNodeSelectorLabel]; exist {
			log.Debug("the node is already labelled", "nodeName", node.Name, "label", localCsiNodeSelectorLabel)
			continue
		}

		node.Labels[localCsiNodeSelectorLabel] = ""

		err = cl.Update(ctx, &node)
		if err != nil {
			log.Error("unable to update the node", logger.Err(err), slog.String("nodeName", node.Name))
			continue
		}

		log.Debug("added the label to the node", "label", localCsiNodeSelectorLabel, "nodeName", node.Name)
	}
}
