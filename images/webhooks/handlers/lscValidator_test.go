/*
Copyright 2026 Flant JSC

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

package handlers

import (
	"context"
	"testing"

	"github.com/slok/kubewebhook/v2/pkg/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/deckhouse/sds-common-lib/fsreserve"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/webhooks/pkg/logger"
)

func TestLSCValidate_DeletingClassIsNotValidated(t *testing.T) {
	now := metav1.Now()
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "deleting",
			DeletionTimestamp: &now,
			Finalizers:        []string{"storage.deckhouse.io/local-storage-class-controller"},
			Annotations:       map[string]string{fsreserve.AnnotationKey: "not-a-number"},
		},
		Spec: slv.LocalStorageClassSpec{
			LVM: &slv.LocalStorageClassLVMSpec{
				Type:            "Thick",
				LVMVolumeGroups: []slv.LocalStorageClassLVG{{Name: "deleted-lvg"}},
			},
		},
	}

	res, err := LSCValidate(logger.NewNop())(context.Background(), &model.AdmissionReview{Operation: model.OperationUpdate}, lsc)
	if err != nil {
		t.Fatalf("LSCValidate() error = %v", err)
	}
	if !res.Valid {
		t.Fatalf("LSCValidate() rejected a class being deleted: %s", res.Message)
	}
}
