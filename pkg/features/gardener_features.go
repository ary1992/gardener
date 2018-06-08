// Copyright (c) 2018 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package features

import (
	utilfeature "k8s.io/apiserver/pkg/util/feature"
)

const (
	// Every feature gate should add method here following this template:
	//
	// // MyFeature enable Foo.
	// // owner: @username
	// // alpha: v5.X
	// MyFeature utilfeature.Feature = "MyFeature"

	// CoreDNS enable CoreDNS as default DNS provider.
	// owner: @mvladev
	// alpha: v5.0
	CoreDNS utilfeature.Feature = "CoreDNS"

	// DeletionConfirmation enables DeletionConfirmation admission controller
	// and uses standard deletion operations for Garden resources.
	// owner: @mvladev
	// alpha: v5.0
	DeletionConfirmation utilfeature.Feature = "DeletionConfirmation"
)

var (
	// APIServerFeatureGate is a shared global FeatureGate for Gardener APIServer flags.
	// right now the Generic API server uses this feature gate as default
	// TODO change it once it moves to ComponentConfig
	APIServerFeatureGate = utilfeature.DefaultFeatureGate

	// ControllerFeatureGate is a shared global FeatureGate for Gardener Controller Manager flags.
	ControllerFeatureGate = utilfeature.NewFeatureGate()

	apiserverFeatureGates = map[utilfeature.Feature]utilfeature.FeatureSpec{
		DeletionConfirmation: {Default: false, PreRelease: utilfeature.Beta},
	}

	controllerManagerFeatureGates = map[utilfeature.Feature]utilfeature.FeatureSpec{
		CoreDNS:              {Default: false, PreRelease: utilfeature.Alpha},
		DeletionConfirmation: {Default: false, PreRelease: utilfeature.Beta},
	}
)

// RegisterAPIServerFeatureGate registers the feature gates
// of the Gardener API Server.
func RegisterAPIServerFeatureGate() {
	APIServerFeatureGate.Add(apiserverFeatureGates)
}

// RegisterControllerFeatureGate registers the feature gates
// of the Gardener Controller Manager.
func RegisterControllerFeatureGate() {
	ControllerFeatureGate.Add(controllerManagerFeatureGates)
}
