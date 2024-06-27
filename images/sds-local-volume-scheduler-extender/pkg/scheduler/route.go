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

package scheduler

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/api/resource"
	"net/http"
	"sds-local-volume-scheduler-extender/pkg/cache"
	"sds-local-volume-scheduler-extender/pkg/logger"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type scheduler struct {
	defaultDivisor float64
	log            logger.Logger
	client         client.Client
	ctx            context.Context
	cache          *cache.Cache
	requestCount   int
}

func (s *scheduler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/filter":
		s.log.Debug("[ServeHTTP] filter route starts handling the request")
		s.filter(w, r)
		s.log.Debug("[ServeHTTP] filter route ends handling the request")
	case "/prioritize":
		s.log.Debug("[ServeHTTP] prioritize route starts handling the request")
		s.prioritize(w, r)
		s.log.Debug("[ServeHTTP] prioritize route ends handling the request")
	case "/status":
		s.log.Debug("[ServeHTTP] status route starts handling the request")
		status(w, r)
		s.log.Debug("[ServeHTTP] status route ends handling the request")
	case "/cache":
		s.log.Debug("[ServeHTTP] cache route starts handling the request")
		s.getCache(w, r)
		s.log.Debug("[ServeHTTP] cache route ends handling the request")
	case "/stat":
		s.log.Debug("[ServeHTTP] stat route starts handling the request")
		s.getCacheStat(w, r)
		s.log.Debug("[ServeHTTP] stat route ends handling the request")
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// NewHandler return new http.Handler of the scheduler extender
func NewHandler(ctx context.Context, cl client.Client, log logger.Logger, lvgCache *cache.Cache, defaultDiv float64) (http.Handler, error) {
	return &scheduler{
		defaultDivisor: defaultDiv,
		log:            log,
		client:         cl,
		ctx:            ctx,
		cache:          lvgCache,
	}, nil
}

func status(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte("ok"))
	if err != nil {
		fmt.Println(fmt.Sprintf("error occurs on status route, err: %s", err.Error()))
	}
}

func (s *scheduler) getCache(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)

	s.cache.PrintTheCacheLog()

	result := make(map[string][]struct {
		pvcName      string
		selectedNode string
		status       string
		size         string
	})

	lvgs := s.cache.GetAllLVG()
	for _, lvg := range lvgs {
		pvcs, err := s.cache.GetAllThickPVCForLVG(lvg.Name)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			s.log.Error(err, "something bad")
		}

		result[lvg.Name] = make([]struct {
			pvcName      string
			selectedNode string
			status       string
			size         string
		}, 0)

		for _, pvc := range pvcs {
			result[lvg.Name] = append(result[lvg.Name], struct {
				pvcName      string
				selectedNode string
				status       string
				size         string
			}{pvcName: pvc.Name, selectedNode: pvc.Annotations[cache.SelectedNodeAnnotation], status: string(pvc.Status.Phase), size: pvc.Spec.Resources.Requests.Storage().String()})
		}
	}

	for lvgName, pvcs := range result {
		reserved, err := s.cache.GetLVGThickReservedSpace(lvgName)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, err = w.Write([]byte("unable to write the cache"))
			if err != nil {
				s.log.Error(err, "error write response")
			}
		}

		_, err = w.Write([]byte(fmt.Sprintf("LVMVolumeGroup: %s Reserved: %s\n", lvgName, resource.NewQuantity(reserved, resource.BinarySI))))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, err = w.Write([]byte("unable to write the cache"))
			if err != nil {
				s.log.Error(err, "error write response")
			}
		}

		for _, pvc := range pvcs {
			_, err = w.Write([]byte(fmt.Sprintf("\tPVC: %s\n", pvc.pvcName)))
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				s.log.Error(err, "error write response")
			}
			_, err = w.Write([]byte(fmt.Sprintf("\t\tNodeName: %s\n", pvc.selectedNode)))
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				s.log.Error(err, "error write response")
			}
			_, err = w.Write([]byte(fmt.Sprintf("\t\tStatus: %s\n", pvc.status)))
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				s.log.Error(err, "error write response")
			}
			_, err = w.Write([]byte(fmt.Sprintf("\t\tSize: %s\n", pvc.size)))
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				s.log.Error(err, "error write response")
			}
		}
	}
}

func (s *scheduler) getCacheStat(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)

	pvcTotalCount := 0
	lvgs := s.cache.GetAllLVG()
	for _, lvg := range lvgs {
		pvcs, err := s.cache.GetAllThickPVCForLVG(lvg.Name)
		if err != nil {
			s.log.Error(err, "something bad")
		}

		pvcTotalCount += len(pvcs)
	}

	_, err := w.Write([]byte(fmt.Sprintf("Filter request count: %d , PVC Count from ALL LVG: %d", s.requestCount, pvcTotalCount)))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, err = w.Write([]byte("unable to write the cache"))
		if err != nil {
			s.log.Error(err, "error write response")
		}
	}
}
