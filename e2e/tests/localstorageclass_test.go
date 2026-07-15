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

package tests

import (
	"context"
	"sort"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
)

var _ = Describe("sds-local-volume LocalStorageClass provisioning", Ordered, func() {
	It("provisions a PVC through a name-based LocalStorageClass", func() {
		ctx, cancel := context.WithTimeout(context.Background(), lscCreatedTimeout+pvcBindTimeout+podRunningTimeout+2*time.Minute)
		defer cancel()

		const scName = "e2e-lsc-by-name"

		By("Creating the name-based LocalStorageClass")
		Expect(storagekube.CreateLocalStorageClass(ctx, suiteRestCfg, storagekube.LocalStorageClassConfig{
			Name:            scName,
			LVMVolumeGroups: []string{suiteLVGs[0]},
			LVMType:         "Thick",
		})).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { deleteLSC(ctx, scName) })

		Expect(storagekube.WaitForLocalStorageClassCreated(ctx, suiteRestCfg, scName, lscCreatedTimeout)).To(Succeed())
		assertStorageClassExists(ctx, scName)

		roundTripPVC(ctx, scName, "e2e-name")
	})

	It("resolves and provisions a matchLabels selector (all matching LVMVolumeGroups)", func() {
		ctx, cancel := context.WithTimeout(context.Background(), lscCreatedTimeout+pvcBindTimeout+podRunningTimeout+2*time.Minute)
		defer cancel()

		const scName = "e2e-lsc-matchlabels"

		By("Creating a matchLabels LocalStorageClass")
		Expect(createLSCWithSelector(ctx, scName, selMatchLabels(map[string]string{poolLabelKey: poolLabelValue}))).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { deleteLSC(ctx, scName) })

		Expect(storagekube.WaitForLocalStorageClassCreated(ctx, suiteRestCfg, scName, lscCreatedTimeout)).To(Succeed())
		assertStorageClassExists(ctx, scName)
		assertResolvedLVGs(ctx, scName, suiteLVGs) // matches every created LVMVolumeGroup

		roundTripPVC(ctx, scName, "e2e-matchlabels")
	})

	It("resolves and provisions a matchExpressions In selector (inclusion of a subset)", func() {
		ctx, cancel := context.WithTimeout(context.Background(), lscCreatedTimeout+pvcBindTimeout+podRunningTimeout+2*time.Minute)
		defer cancel()

		const scName = "e2e-lsc-in"

		By("Creating a matchExpressions In LocalStorageClass")
		Expect(createLSCWithSelector(ctx, scName, selMatchExpr(tierLabelKey, "In", []string{tierFast}))).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { deleteLSC(ctx, scName) })

		Expect(storagekube.WaitForLocalStorageClassCreated(ctx, suiteRestCfg, scName, lscCreatedTimeout)).To(Succeed())
		assertStorageClassExists(ctx, scName)
		assertResolvedLVGs(ctx, scName, []string{suiteLVGs[0]}) // only the "fast" LVMVolumeGroup

		roundTripPVC(ctx, scName, "e2e-in")
	})

	It("resolves and provisions a matchExpressions NotIn selector (exclusion of a subset)", func() {
		if len(suiteLVGs) < 2 {
			Skip("need >= 2 LVMVolumeGroups to exercise exclusion")
		}
		ctx, cancel := context.WithTimeout(context.Background(), lscCreatedTimeout+pvcBindTimeout+podRunningTimeout+2*time.Minute)
		defer cancel()

		const scName = "e2e-lsc-notin"

		By("Creating a matchExpressions NotIn LocalStorageClass")
		Expect(createLSCWithSelector(ctx, scName, selMatchExpr(tierLabelKey, "NotIn", []string{tierFast}))).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { deleteLSC(ctx, scName) })

		Expect(storagekube.WaitForLocalStorageClassCreated(ctx, suiteRestCfg, scName, lscCreatedTimeout)).To(Succeed())
		assertStorageClassExists(ctx, scName)
		assertResolvedLVGs(ctx, scName, suiteLVGs[1:]) // every LVMVolumeGroup except the "fast" one

		roundTripPVC(ctx, scName, "e2e-notin")
	})
})

// selMatchLabels builds the unstructured form of a matchLabels label selector.
func selMatchLabels(m map[string]string) map[string]interface{} {
	ml := make(map[string]interface{}, len(m))
	for k, v := range m {
		ml[k] = v
	}
	return map[string]interface{}{"matchLabels": ml}
}

// selMatchExpr builds the unstructured form of a single-requirement
// matchExpressions label selector.
func selMatchExpr(key, operator string, values []string) map[string]interface{} {
	vals := make([]interface{}, len(values))
	for i, v := range values {
		vals[i] = v
	}
	return map[string]interface{}{
		"matchExpressions": []interface{}{
			map[string]interface{}{"key": key, "operator": operator, "values": vals},
		},
	}
}

// createLSCWithSelector creates a LocalStorageClass whose single lvmVolumeGroups
// entry selects LVMVolumeGroups by the given label selector. The storage-e2e
// helper only supports name-based entries, so the selector form is built here.
func createLSCWithSelector(ctx context.Context, name string, selector map[string]interface{}) error {
	lsc := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "storage.deckhouse.io/v1alpha1",
			"kind":       "LocalStorageClass",
			"metadata":   map[string]interface{}{"name": name},
			"spec": map[string]interface{}{
				"reclaimPolicy":     "Delete",
				"volumeBindingMode": "WaitForFirstConsumer",
				"lvm": map[string]interface{}{
					"type": "Thick",
					"lvmVolumeGroups": []interface{}{
						map[string]interface{}{"labelSelector": selector},
					},
				},
			},
		},
	}
	_, err := suiteDyn.Resource(storagekube.LocalStorageClassGVR).Create(ctx, lsc, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// lvgParamKey is the StorageClass parameter the controller writes the resolved
// LVMVolumeGroup list into (the CSI/scheduler contract).
const lvgParamKey = "local.csi.storage.deckhouse.io/lvm-volume-groups"

// resolvedLVGNames returns the LVMVolumeGroup names the controller resolved into
// the managed StorageClass parameter, sorted.
func resolvedLVGNames(ctx context.Context, scName string) ([]string, error) {
	var sc storagev1.StorageClass
	if err := suiteK8s.Get(ctx, client.ObjectKey{Name: scName}, &sc); err != nil {
		return nil, err
	}
	var entries []struct {
		Name string `json:"name"`
	}
	if err := yaml.Unmarshal([]byte(sc.Parameters[lvgParamKey]), &entries); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	sort.Strings(names)
	return names, nil
}

// assertResolvedLVGs asserts the managed StorageClass resolved to exactly want.
func assertResolvedLVGs(ctx context.Context, scName string, want []string) {
	GinkgoHelper()
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	Eventually(func() ([]string, error) {
		return resolvedLVGNames(ctx, scName)
	}).WithTimeout(lscCreatedTimeout).WithPolling(pollInterval).
		Should(Equal(sorted), "StorageClass %s should resolve to %v", scName, sorted)
}

func deleteLSC(ctx context.Context, name string) {
	err := suiteDyn.Resource(storagekube.LocalStorageClassGVR).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		GinkgoWriter.Printf("  warning: LocalStorageClass %s cleanup failed: %v\n", name, err)
	}
}

func assertStorageClassExists(ctx context.Context, name string) {
	GinkgoHelper()
	var sc storagev1.StorageClass
	Expect(suiteK8s.Get(ctx, client.ObjectKey{Name: name}, &sc)).
		To(Succeed(), "managed StorageClass %s should exist", name)
	Expect(sc.Provisioner).To(Equal("local.csi.storage.deckhouse.io"))
}

// roundTripPVC creates a PVC + Pod on the given StorageClass, waits for the PVC
// to bind and the Pod to run (proving the local CSI driver provisioned and
// mounted the volume), then tears them down.
func roundTripPVC(ctx context.Context, scName, prefix string) {
	GinkgoHelper()
	pvcName := prefix + "-pvc"
	podName := prefix + "-pod"

	By("Creating the PVC and consumer Pod")
	Expect(suiteK8s.Create(ctx, buildPVC(pvcName, scName))).To(Succeed())
	DeferCleanup(func(ctx SpecContext) { deletePVCAndPod(ctx, pvcName, podName) })
	Expect(suiteK8s.Create(ctx, buildPod(podName, pvcName))).To(Succeed())

	By("Waiting for the Pod to run and the PVC to bind")
	Eventually(func() (corev1.PodPhase, error) {
		var pod corev1.Pod
		if err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: podName}, &pod); err != nil {
			return "", err
		}
		return pod.Status.Phase, nil
	}).WithTimeout(pvcBindTimeout+podRunningTimeout).WithPolling(pollInterval).
		Should(Equal(corev1.PodRunning), "consumer Pod should reach Running")

	var pvc corev1.PersistentVolumeClaim
	Expect(suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: pvcName}, &pvc)).To(Succeed())
	Expect(pvc.Status.Phase).To(Equal(corev1.ClaimBound), "PVC should be Bound")
}

func buildPVC(name, scName string) *corev1.PersistentVolumeClaim {
	sc := scName
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: suiteCfg.namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &sc,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(suiteCfg.pvcSize),
				},
			},
		},
	}
}

func buildPod(name, pvcName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: suiteCfg.namespace},
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: ptrInt64(2),
			Containers: []corev1.Container{{
				Name:    probeContainerName,
				Image:   probeImage,
				Command: []string{"sh", "-c", "echo e2e > /data/probe && sync && sleep 3600"},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "data",
					MountPath: "/data",
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName},
				},
			}},
		},
	}
}

func deletePVCAndPod(ctx context.Context, pvcName, podName string) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: suiteCfg.namespace}}
	if err := suiteK8s.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		GinkgoWriter.Printf("  warning: Pod %s cleanup failed: %v\n", podName, err)
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: suiteCfg.namespace}}
	if err := suiteK8s.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
		GinkgoWriter.Printf("  warning: PVC %s cleanup failed: %v\n", pvcName, err)
	}
	// Best-effort wait for the PVC to be gone so the LVMVolumeGroup can be reused.
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var cur corev1.PersistentVolumeClaim
		if err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: pvcName}, &cur); apierrors.IsNotFound(err) {
			return
		}
		if !sleepCtx(ctx, pollInterval) {
			return
		}
	}
}

func ptrInt64(v int64) *int64 { return &v }
