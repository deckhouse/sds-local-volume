package scheduler

import (
	"fmt"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

type scheduler struct {
	defaultDivisor float64
	divisors       map[string]float64
	client         client.Client
}

func (s scheduler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/filter":
		fmt.Println("************************")
		fmt.Println("TIME NOW " + time.Now().String())
		fmt.Println("FILTER SERVE")
		fmt.Println("************************")
		s.filter(w, r)
	case "/prioritize":
		fmt.Println("************************")
		fmt.Println("PRIORITIZE SERVE")
		fmt.Println("************************")
		s.prioritize(w, r)
	case "/status":
		status(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// NewHandler return new http.Handler of the scheduler extender
func NewHandler(cl client.Client, defaultDiv float64, divisors map[string]float64) (http.Handler, error) {
	for _, divisor := range divisors {
		if divisor <= 0 {
			return nil, fmt.Errorf("invalid divisor: %f", divisor)
		}
	}

	return scheduler{defaultDiv, divisors, cl}, nil
}

func status(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
