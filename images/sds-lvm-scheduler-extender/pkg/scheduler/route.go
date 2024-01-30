package scheduler

import (
	"fmt"
	"net/http"
	"sds-lvm-scheduler-extender/pkg/logger"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type scheduler struct {
	defaultDivisor float64
	log            logger.Logger
	client         client.Client
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
func NewHandler(cl client.Client, log logger.Logger, defaultDiv float64) (http.Handler, error) {
	return scheduler{defaultDiv, log, cl}, nil
}

func status(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte("ok"))
	if err != nil {
		fmt.Println(fmt.Sprintf("error occurs on status route, err: %s", err.Error()))
	}
}
