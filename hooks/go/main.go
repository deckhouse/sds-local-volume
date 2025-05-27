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

package main

import (
	"github.com/deckhouse/module-sdk/pkg/app"
	_ "github.com/deckhouse/sds-local-volume/hooks/go/020-webhook-certs"
<<<<<<< HEAD
	_ "github.com/deckhouse/sds-local-volume/hooks/go/070-generate-certs"
	_ "github.com/deckhouse/sds-local-volume/hooks/go/090-on-start-checks"
=======
	_ "github.com/deckhouse/sds-local-volume/hooks/go/030-enable-thin-provisioning"
<<<<<<< HEAD
	_ "github.com/deckhouse/sds-local-volume/hooks/go/070-generate-certs"
>>>>>>> b3e72c2 (added certs for scheduler_extender and stuff)
=======
>>>>>>> 65ebfbf (fix)
)

func main() {
	app.Run()
}
