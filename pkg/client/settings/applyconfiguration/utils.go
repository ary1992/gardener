/*
Copyright SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file

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

// Code generated by applyconfiguration-gen. DO NOT EDIT.

package applyconfiguration

import (
	v1alpha1 "github.com/gardener/gardener/pkg/apis/settings/v1alpha1"
	settingsv1alpha1 "github.com/gardener/gardener/pkg/client/settings/applyconfiguration/settings/v1alpha1"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
)

// ForKind returns an apply configuration type for the given GroupVersionKind, or nil if no
// apply configuration type exists for the given GroupVersionKind.
func ForKind(kind schema.GroupVersionKind) interface{} {
	switch kind {
	// Group=settings.gardener.cloud, Version=v1alpha1
	case v1alpha1.SchemeGroupVersion.WithKind("ClusterOpenIDConnectPreset"):
		return &settingsv1alpha1.ClusterOpenIDConnectPresetApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("ClusterOpenIDConnectPresetSpec"):
		return &settingsv1alpha1.ClusterOpenIDConnectPresetSpecApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("KubeAPIServerOpenIDConnect"):
		return &settingsv1alpha1.KubeAPIServerOpenIDConnectApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("OpenIDConnectClientAuthentication"):
		return &settingsv1alpha1.OpenIDConnectClientAuthenticationApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("OpenIDConnectPreset"):
		return &settingsv1alpha1.OpenIDConnectPresetApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("OpenIDConnectPresetSpec"):
		return &settingsv1alpha1.OpenIDConnectPresetSpecApplyConfiguration{}

	}
	return nil
}
