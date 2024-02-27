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
	"net/http"
	"sds-local-volume-scheduler-extender/pkg/logger"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type scheduler struct {
	defaultDivisor float64
	log            logger.Logger
	client         client.Client
	ctx            context.Context
}

func (s scheduler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// NewHandler return new http.Handler of the scheduler extender
func NewHandler(ctx context.Context, cl client.Client, log logger.Logger, defaultDiv float64) (http.Handler, error) {
	return scheduler{
		defaultDivisor: defaultDiv,
		log:            log,
		client:         cl,
		ctx:            ctx,
	}, nil
}

func status(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte("ok"))
	if err != nil {
		fmt.Println(fmt.Sprintf("error occurs on status route, err: %s", err.Error()))
	}
}
