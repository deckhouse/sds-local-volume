package controller

import (
	"context"
	"testing"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sds-local-volume-controller/pkg/logger"
)

func TestRunLocalCSINodeWatcherController(t *testing.T) {
	cl := NewFakeClient()
	ctx := context.Background()
	log := logger.Logger{}

	t.Run("getSecret_returns_secret", func(t *testing.T) {
		secretName := "test-name"
		secretNamespace := "test-ns"
		secret := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: secretNamespace,
			},
		}

		err := cl.Create(ctx, secret)
		if err != nil {
			t.Error(err)
		}

		actual, err := getSecret(ctx, cl, secretNamespace, secretName)
		if assert.NoError(t, err) {
			assert.Equal(t, secretNamespace, actual.Namespace)
			assert.Equal(t, secretName, actual.Name)
		}
	})

	t.Run("reconcileLocalCSINodes_returns_no_error", func(t *testing.T) {
		secretName := "test-name1"
		secretNamespace := "test-ns"
		data := `nodeSelector:
  mycustomlabel: test`
		secret := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: secretNamespace,
			},
			Data: map[string][]byte{
				"config": []byte(data),
			},
		}

		err := cl.Create(ctx, secret)
		if err != nil {
			t.Error(err)
		}

		lvgOnNode4 := &snc.LvmVolumeGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name: "lvgOnNode4",
			},
			Status: snc.LvmVolumeGroupStatus{
				Nodes: []snc.LvmVolumeGroupNode{
					{
						Name: "test-node4",
					},
				},
			},
		}
		err = cl.Create(ctx, lvgOnNode4)
		if err != nil {
			t.Error(err)
		}

		lsc := &slv.LocalStorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-lsc",
			},
			Spec: slv.LocalStorageClassSpec{
				LVM: &slv.LocalStorageClassLVMSpec{
					LVMVolumeGroups: []slv.LocalStorageClassLVG{
						{
							Name: "lvgOnNode4",
						},
					},
				},
			},
		}
		err = cl.Create(ctx, lsc)
		if err != nil {
			t.Error(err)
		}

		nodes := []v1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node1",
					Labels: map[string]string{
						"mycustomlabel":         "test",
						nodeManualEvictionLabel: "",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node2",
					Labels: map[string]string{
						"mycustomlabel": "test",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node3",
					Labels: map[string]string{
						"nothing": "forme",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node4",
					Labels: map[string]string{
						localCsiNodeSelectorLabel: "",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node5",
					Labels: map[string]string{
						localCsiNodeSelectorLabel: "",
					},
				},
			},
		}

		for _, n := range nodes {
			err = cl.Create(ctx, &n)
			if err != nil {
				t.Error(err)
			}
		}

		err = reconcileLocalCSINodes(ctx, cl, log, secret)
		if assert.NoError(t, err) {
			nodeList := &v1.NodeList{}
			err = cl.List(ctx, nodeList)
			if err != nil {
				t.Error(err)
			}

			nodeMap := make(map[string]v1.Node, len(nodeList.Items))
			for _, n := range nodeList.Items {
				nodeMap[n.Name] = n
			}

			// assert that healthy node lost nodeManualEvictionLabel
			node1 := nodeMap["test-node1"]
			_, exist := node1.Labels[nodeManualEvictionLabel]
			assert.False(t, exist)

			// assert that nodes with a selector has localCsiNodeSelectorLabel
			node2 := nodeMap["test-node2"]
			_, exist = node2.Labels[nodeManualEvictionLabel]
			assert.False(t, exist)
			_, exist = node2.Labels[localCsiNodeSelectorLabel]
			assert.True(t, exist)

			// assert that node without selector has nothing
			node3 := nodeMap["test-node3"]
			_, exist = node3.Labels[nodeManualEvictionLabel]
			assert.False(t, exist)
			_, exist = node3.Labels[localCsiNodeSelectorLabel]
			assert.False(t, exist)

			// assert that node with lvg and lsc does not lose localCsiNodeSelectorLabel and has nodeManualEvictionLabel
			// just like its dependent resources
			node4 := nodeMap["test-node4"]
			_, exist = node4.Labels[nodeManualEvictionLabel]
			assert.True(t, exist)
			_, exist = node4.Labels[localCsiNodeSelectorLabel]
			assert.True(t, exist)

			updateLvg := &snc.LvmVolumeGroup{}
			err = cl.Get(ctx,
				client.ObjectKey{
					Name: "lvgOnNode4",
				}, updateLvg)
			if err != nil {
				t.Error(err)
			}
			_, exist = updateLvg.Labels[candidateManualEvictionLabel]
			assert.True(t, exist)

			updatedLsc := &slv.LocalStorageClass{}
			err = cl.Get(ctx,
				client.ObjectKey{
					Name: "test-lsc",
				}, updatedLsc)
			if err != nil {
				t.Error(err)
			}
			_, exist = updateLvg.Labels[candidateManualEvictionLabel]
			assert.True(t, exist)

			// assert that node without any resources lost localCsiNodeSelectorLabel
			node5 := nodeMap["test-node5"]
			_, exist = node5.Labels[nodeManualEvictionLabel]
			assert.False(t, exist)
			_, exist = node5.Labels[localCsiNodeSelectorLabel]
			assert.False(t, exist)
		}
	})
}

func NewFakeClient() client.WithWatch {
	s := scheme.Scheme
	_ = metav1.AddMetaToScheme(s)
	_ = slv.AddToScheme(s)
	_ = snc.AddToScheme(s)

	builder := fake.NewClientBuilder().WithScheme(s)

	cl := builder.Build()
	return cl
}
