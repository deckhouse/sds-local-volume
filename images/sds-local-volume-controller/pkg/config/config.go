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

package config

import (
	"os"
	"sds-local-volume-controller/pkg/logger"
	"time"
)

const (
	LogLevel        = "LOG_LEVEL"
	RequeueInterval = "REQUEUE_INTERVAL"
)

type Options struct {
	Loglevel        logger.Verbosity
	RequeueInterval time.Duration
}

func NewConfig() *Options {
	var opts Options

	loglevel := os.Getenv(LogLevel)
	if loglevel == "" {
		opts.Loglevel = logger.DebugLevel
	} else {
		opts.Loglevel = logger.Verbosity(loglevel)
	}

	opts.RequeueInterval = 10

	return &opts
}
