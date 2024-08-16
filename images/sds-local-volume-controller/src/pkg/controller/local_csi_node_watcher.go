/*
Copyright 2024 Flant JSC

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
	"strings"
	"time"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/yaml"

	"sds-local-volume-controller/pkg/config"
	"sds-local-volume-controller/pkg/logger"
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
) (controller.Controller, error) {
	cl := mgr.GetClient()

	c, err := controller.New(LocalCSINodeWatcherCtrl, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			log.Info(fmt.Sprintf("[RunLocalCSINodeWatcherController] Reconciler func starts a reconciliation for the request: %s", request.NamespacedName.String()))
			if request.Name == cfg.ConfigSecretName {
				log.Debug(fmt.Sprintf("[RunLocalCSINodeWatcherController] request name %s matches the target config secret name %s. Start to reconcile", request.Name, cfg.ConfigSecretName))

				log.Debug(fmt.Sprintf("[RunLocalCSINodeWatcherController] tries to get a secret by the request %s", request.NamespacedName.String()))
				secret, err := getSecret(ctx, cl, request.Namespace, request.Name)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunLocalCSINodeWatcherController] unable to get a secret by the request %s", request.NamespacedName.String()))
					return reconcile.Result{}, err
				}
				log.Debug(fmt.Sprintf("[RunLocalCSINodeWatcherController] successfully got a secret by the request %s", request.NamespacedName.String()))

				log.Debug(fmt.Sprintf("[RunLocalCSINodeWatcherController] tries to reconcile local CSI nodes for the secret %s/%s", secret.Namespace, secret.Name))
				err = reconcileLocalCSINodes(ctx, cl, log, secret)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunLocalCSINodeWatcherController] unable to reconcile local CSI nodes for the secret %s/%s", secret.Namespace, secret.Name))
					return reconcile.Result{}, err
				}
				log.Debug(fmt.Sprintf("[RunLocalCSINodeWatcherController] successfully reconciled local CSI nodes for the secret %s/%s", secret.Namespace, secret.Name))

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
	log.Debug("[reconcileLocalCSINodes] tries to get a selector from the config")
	nodeSelector, err := getNodeSelectorFromConfig(secret)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLocalCSINodes] unable to get node selector from the secret %s/%s", secret.Namespace, secret.Name))
		return err
	}
	log.Trace(fmt.Sprintf("[labelNodesWithLocalCSIIfNeeded] node selector from the config: %v", nodeSelector))
	log.Debug("[reconcileLocalCSINodes] successfully got a selector from the config")

	log.Debug(fmt.Sprintf("[reconcileLocalCSINodes] tries to get kubernetes nodes by the selector %v", nodeSelector))
	nodesWithSelector, err := getKubernetesNodesBySelector(ctx, cl, nodeSelector)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLocalCSINodes] unable to get nodes by selector %v", nodeSelector))
		return err
	}
	for _, n := range nodesWithSelector.Items {
		log.Trace(fmt.Sprintf("[labelNodesWithLocalCSIIfNeeded] node %s has been got by selector %v", n.Name, nodeSelector))
	}
	log.Debug("[reconcileLocalCSINodes] successfully got kubernetes nodes by the selector")

	labelNodesWithLocalCSIIfNeeded(ctx, cl, log, nodesWithSelector)
	log.Debug(fmt.Sprintf("[reconcileLocalCSINodes] finished labeling the selected nodes with a label %s", localCsiNodeSelectorLabel))

	log.Debug(fmt.Sprintf("[reconcileLocalCSINodes] start to clear the nodes without the selector %v", nodeSelector))
	log.Debug("[reconcileLocalCSINodes] tries to get all kubernetes nodes")
	nodes, err := getKubeNodes(ctx, cl)
	if err != nil {
		log.Error(err, "[reconcileLocalCSINodes] unable to get nodes")
		return err
	}
	for _, n := range nodes.Items {
		log.Trace(fmt.Sprintf("[labelNodesWithLocalCSIIfNeeded] node %s has been got", n.Name))
	}
	log.Debug("[reconcileLocalCSINodes] successfully got all kubernetes nodes")

	reconcileLocalCSILabels(ctx, cl, log, nodes, nodeSelector)
	log.Debug(fmt.Sprintf("[reconcileLocalCSINodes] finished removing the label %s from the nodes without the selector %v", localCsiNodeSelectorLabel, nodeSelector))

	return nil
}

func reconcileLocalCSILabels(ctx context.Context, cl client.Client, log logger.Logger, nodes *v1.NodeList, selector map[string]string) {
	var err error
	for _, node := range nodes.Items {
		log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] starts the reconciliation for the node %s", node.Name))
		if labels.Set(selector).AsSelector().Matches(labels.Set(node.Labels)) {
			log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] no need to remove a label %s from the node %s as its labels match the selector", localCsiNodeSelectorLabel, node.Name))

			err = clearManualEvictionLabelsIfNeeded(ctx, cl, log, node)
			if err != nil {
				log.Error(err, fmt.Sprintf("[reconcileLocalCSILabels] unable to remove manual eviction labels %s, %s from the nodes, LVMVolumeGroup and LocalStorageClasses", nodeManualEvictionLabel, candidateManualEvictionLabel))
			}
			continue
		}

		if _, exist := node.Labels[localCsiNodeSelectorLabel]; !exist {
			log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] no need to remove a label %s from the node %s as it does not has the label", localCsiNodeSelectorLabel, node.Name))
			continue
		}

		lvgsForManualEviction, lscsForManualEviction, err := getManuallyEvictedLVGsAndLSCs(ctx, cl, node)
		if err != nil {
			log.Error(err, fmt.Sprintf("[reconcileLocalCSILabels] unable to get LVMVolumeGroups for manual eviction for the node %s", node.Name))
			continue
		}

		if len(lvgsForManualEviction) == 0 &&
			len(lscsForManualEviction) == 0 {
			log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] no dependent resources were found for the node %s. Label %s will be removed from the node", node.Name, localCsiNodeSelectorLabel))

			delete(node.Labels, localCsiNodeSelectorLabel)
			delete(node.Labels, nodeManualEvictionLabel)

			err = cl.Update(ctx, &node)
			if err != nil {
				log.Error(err, fmt.Sprintf("[reconcileLocalCSILabels] unable to update the node %s", node.Name))
				continue
			}

			log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] the label %s has been successfully removed from the node %s", localCsiNodeSelectorLabel, node.Name))
		} else {
			lvgsNames := strings.Builder{}
			for _, lvg := range lvgsForManualEviction {
				lvgsNames.WriteString(fmt.Sprintf("%s ", lvg.Name))
			}
			lscNames := strings.Builder{}
			for _, lsc := range lscsForManualEviction {
				lscNames.WriteString(fmt.Sprintf("%s ", lsc.Name))
			}
			log.Warning(fmt.Sprintf("[reconcileLocalCSILabels] unable to remove label %s from the node %s due to the node's LVMVolumeGroups %s are used in LocalStorageClasses %s", localCsiNodeSelectorLabel, node.Name, lvgsNames.String(), lscNames.String()))

			added, err := addLabelOnTheNodeIfNotExist(ctx, cl, node, nodeManualEvictionLabel)
			if err != nil {
				log.Error(err, fmt.Sprintf("[reconcileLocalCSILabels] unable to update the node %s", node.Name))
				continue
			}
			if !added {
				log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] label %s has been already added to the node %s", nodeManualEvictionLabel, node.Name))
			} else {
				log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] successfully added the label %s to the node %s", nodeManualEvictionLabel, node.Name))
			}

			for _, lvg := range lvgsForManualEviction {
				added, err = addLabelOnTheLVGIfNotExist(ctx, cl, lvg, candidateManualEvictionLabel)
				if err != nil {
					log.Error(err, fmt.Sprintf("[reconcileLocalCSILabels] unable to update the LVMVolumeGroup %s", lvg.Name))
					continue
				}
				if !added {
					log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] label %s has been already added to the LVMVolumeGroup %s", candidateManualEvictionLabel, lvg.Name))
				} else {
					log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] successfully added the label %s to the LVMVolumeGroup %s", candidateManualEvictionLabel, lvg.Name))
				}
			}

			for _, lsc := range lscsForManualEviction {
				added, err = addLabelOnTheLSCIfNotExist(ctx, cl, lsc, candidateManualEvictionLabel)
				if err != nil {
					log.Error(err, fmt.Sprintf("[reconcileLocalCSILabels] unable to update the LocalStorageClass %s", lsc.Name))
					continue
				}
				if !added {
					log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] label %s has been already added to the LocalStorageClass %s", candidateManualEvictionLabel, lsc.Name))
				} else {
					log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] successfully added the label %s to the LocalStorageClass %s", candidateManualEvictionLabel, lsc.Name))
				}
			}
		}
		log.Debug(fmt.Sprintf("[reconcileLocalCSILabels] ends the reconciliation for the node %s", node.Name))
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

func addLabelOnTheLVGIfNotExist(ctx context.Context, cl client.Client, lvg snc.LvmVolumeGroup, label string) (bool, error) {
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
		log.Debug(fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] the node %s does not have the label %s", node.Name, nodeManualEvictionLabel))
	} else {
		log.Debug(fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] the node %s has the label %s. It will be removed", node.Name, nodeManualEvictionLabel))
		delete(node.Labels, nodeManualEvictionLabel)
		err := cl.Update(ctx, &node)
		if err != nil {
			log.Error(err, fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] unable to update the node %s", node.Name))
			return err
		}
	}

	lvgList, err := getLVMVolumeGroups(ctx, cl)
	if err != nil {
		log.Error(err, "[clearManualEvictionLabelsIfNeeded] unable to get LVMVolumeGroups")
		return err
	}

	lvgs := make(map[string]snc.LvmVolumeGroup, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		lvgs[lvg.Name] = lvg
	}

	usedLvgs := make(map[string]snc.LvmVolumeGroup, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		for _, n := range lvg.Status.Nodes {
			if n.Name == node.Name {
				usedLvgs[lvg.Name] = lvg
			}
		}
	}

	log.Debug(fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] starts the removing label %s from the LVMVolumeGroups for the node %s", candidateManualEvictionLabel, node.Name))
	for _, lvg := range usedLvgs {
		if _, exist := lvg.Labels[candidateManualEvictionLabel]; !exist {
			log.Debug(fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] the LVMVolumeGroup %s does not has the label %s", lvg.Name, candidateManualEvictionLabel))
			continue
		}

		log.Debug(fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] the LVMVolumeGroup %s has a label %s. It will be removed", lvg.Name, candidateManualEvictionLabel))
		delete(lvg.Labels, candidateManualEvictionLabel)
		err = cl.Update(ctx, &lvg)
		if err != nil {
			log.Error(err, fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] unable to update the LVMVolumeGroup %s", lvg.Name))
			continue
		}
		log.Debug(fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] the LVMVolumeGroup %s label %s has been successfully removed", lvg.Name, candidateManualEvictionLabel))
	}
	log.Debug(fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] finished the removing label %s from the LVMVolumeGroups for the node %s", candidateManualEvictionLabel, node.Name))

	lscList, err := getLocalStorageClasses(ctx, cl)
	if err != nil {
		return err
	}

	for _, lsc := range lscList.Items {
		if _, exist := lsc.Labels[candidateManualEvictionLabel]; !exist {
			log.Debug(fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] the LocalStorageClass %s does not have the label %s", lsc.Name, candidateManualEvictionLabel))
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
			log.Debug(fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] the LocalStorageClass has manually evicted LVMVolumeGroups %s. The label %s will not be removed", badLVGs.String(), candidateManualEvictionLabel))
		} else {
			log.Debug(fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] the LocalStorageClass does not have any manually evicted LVMVolumeGroup. The label %s will be removed", candidateManualEvictionLabel))

			delete(lsc.Labels, candidateManualEvictionLabel)
			err = cl.Update(ctx, &lsc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] unable to update the LocalStorageClass %s", lsc.Name))
				continue
			}
			log.Debug(fmt.Sprintf("[clearManualEvictionLabelsIfNeeded] successfully removed the label %s from the LocalStorageClass %s", candidateManualEvictionLabel, lsc.Name))
		}
	}

	return nil
}

func getManuallyEvictedLVGsAndLSCs(ctx context.Context, cl client.Client, node v1.Node) (map[string]snc.LvmVolumeGroup, map[string]slv.LocalStorageClass, error) {
	lvgList, err := getLVMVolumeGroups(ctx, cl)
	if err != nil {
		return nil, nil, err
	}

	usedLvgs := make(map[string]snc.LvmVolumeGroup, len(lvgList.Items))
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
	unhealthyLvgs := make(map[string]snc.LvmVolumeGroup, len(usedLvgs))

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

func getLVMVolumeGroups(ctx context.Context, cl client.Client) (*snc.LvmVolumeGroupList, error) {
	lvgList := &snc.LvmVolumeGroupList{}
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
			log.Debug(fmt.Sprintf("[labelNodesWithLocalCSIIfNeeded] a node %s has already been labeled with label %s", node.Name, localCsiNodeSelectorLabel))
			continue
		}

		node.Labels[localCsiNodeSelectorLabel] = ""

		err = cl.Update(ctx, &node)
		if err != nil {
			log.Error(err, fmt.Sprintf("[labelNodesWithLocalCSIIfNeeded] unable to update a node %s", node.Name))
			continue
		}

		log.Debug(fmt.Sprintf("[labelNodesWithLocalCSIIfNeeded] successufully added label %s to the node %s", localCsiNodeSelectorLabel, node.Name))
	}
}
