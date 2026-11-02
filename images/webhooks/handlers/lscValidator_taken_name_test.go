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
	"errors"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestForeignStorageClass(t *testing.T) {
	const name = "local-thick"
	storageClass := func(provisioner string) *storagev1.StorageClass {
		return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Provisioner: provisioner}
	}

	tests := []struct {
		name            string
		existing        []client.Object
		getErr          error
		wantProvisioner string
		wantErr         bool
	}{
		{name: "no StorageClass with the name"},
		{name: "StorageClass of the module's provisioner", existing: []client.Object{storageClass(localCSIProvisioner)}},
		{name: "StorageClass of another provisioner", existing: []client.Object{storageClass("nfs.csi.k8s.io")}, wantProvisioner: "nfs.csi.k8s.io"},
		{name: "the StorageClass cannot be read", getErr: errors.New("forbidden"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := apiruntime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existing...).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if tt.getErr != nil {
							return tt.getErr
						}
						return c.Get(ctx, key, obj, opts...)
					},
				}).
				Build()

			sc, err := foreignStorageClass(context.Background(), cl, name)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", sc)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantProvisioner == "" {
				if sc != nil {
					t.Fatalf("expected the name to be free, got the StorageClass %+v", sc)
				}
				return
			}
			if sc == nil || sc.Provisioner != tt.wantProvisioner {
				t.Fatalf("expected the StorageClass of %q, got %+v", tt.wantProvisioner, sc)
			}
		})
	}
}
