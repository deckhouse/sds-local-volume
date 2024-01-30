package cmd

import (
	"context"
	"errors"
	"fmt"
	v1 "k8s.io/api/core/v1"
	sv1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"net/http"
	"os"
	"os/signal"
	"sds-lvm-scheduler-extender/pkg/api/v1alpha1"
	"sds-lvm-scheduler-extender/pkg/kubutils"
	"sds-lvm-scheduler-extender/pkg/logger"
	"sds-lvm-scheduler-extender/pkg/scheduler"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sync"
	"syscall"
	"time"

	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var cfgFilePath string

var resourcesSchemeFuncs = []func(*apiruntime.Scheme) error{
	v1alpha1.AddToScheme,
	clientgoscheme.AddToScheme,
	extv1.AddToScheme,
	v1.AddToScheme,
	sv1.AddToScheme,
}

const (
	defaultDivisor    = 1
	defaultListenAddr = ":8000"
)

type Config struct {
	ListenAddr     string  `json:"listen"`
	DefaultDivisor float64 `json:"default-divisor"`
	LogLevel       string  `json:"log-level"`
}

var config = &Config{
	ListenAddr:     defaultListenAddr,
	DefaultDivisor: defaultDivisor,
	LogLevel:       "2",
}

var rootCmd = &cobra.Command{
	Use:     "sds-lvm-scheduler",
	Version: "development",
	Short:   "a scheduler-extender for SDS-LVM",
	Long: `A scheduler-extender for SDS-LVM.
The extender implements filter and prioritize verbs.
The filter verb is "filter" and served at "/filter" via HTTP.
It filters out nodes that have less storage capacity than requested.
The requested capacity is read from "capacity.topolvm.io/<device-class>"
resource value.
The prioritize verb is "prioritize" and served at "/prioritize" via HTTP.
It scores nodes with this formula:
    min(10, max(0, log2(capacity >> 30 / divisor)))
The default divisor is 1.  It can be changed with a command-line option.
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return subMain(cmd.Context())
	},
}

func subMain(parentCtx context.Context) error {
	if len(cfgFilePath) != 0 {
		b, err := os.ReadFile(cfgFilePath)
		if err != nil {
			return err
		}
		err = yaml.Unmarshal(b, config)
		if err != nil {
			return err
		}
	}

	log, err := logger.NewLogger(logger.Verbosity(config.LogLevel))
	if err != nil {
		fmt.Println(fmt.Sprintf("[subMain] unable to initialize logger, err: %s", err.Error()))
	}
	log.Info(fmt.Sprintf("[subMain] logger has been initialized, log level: %s", config.LogLevel))
	ctrl.SetLogger(log.GetLogger())

	kConfig, err := kubutils.KubernetesDefaultConfigCreate()
	if err != nil {
		log.Error(err, "[subMain] unable to KubernetesDefaultConfigCreate")
	}
	log.Info("[subMain] kubernetes config has been successfully created.")

	scheme := runtime.NewScheme()
	for _, f := range resourcesSchemeFuncs {
		err := f(scheme)
		if err != nil {
			log.Error(err, "[subMain] unable to add scheme to func")
			os.Exit(1)
		}
	}
	log.Info("[subMain] successfully read scheme CR")

	cl, err := client.New(kConfig, client.Options{
		Scheme:         scheme,
		WarningHandler: client.WarningHandlerOptions{},
	})

	h, err := scheduler.NewHandler(cl, *log, config.DefaultDivisor)
	if err != nil {
		return err
	}
	log.Info("[subMain] scheduler handler initialized")

	serv := &http.Server{
		Addr:        config.ListenAddr,
		Handler:     accessLogHandler(parentCtx, h),
		ReadTimeout: 30 * time.Second,
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	ctx, stop := signal.NotifyContext(parentCtx, os.Interrupt, syscall.SIGTERM)
	defer stop() // stop() should be called before wg.Wait() to stop the goroutine correctly.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		if err := serv.Shutdown(parentCtx); err != nil {
			log.Error(err, "failed to shutdown gracefully")
		}
	}()

	log.Info(fmt.Sprintf("[subMain] starts serving on: %s", config.ListenAddr))
	err = serv.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFilePath, "config", "", "config file")
}
