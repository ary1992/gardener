// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

//go:generate ../hack/generate-imagename-constants.sh imagevector containers.yaml Container
//go:generate ../hack/resolve-etcd-version-from-etcd-druid.sh containers.yaml
//go:generate ../hack/generate-imagename-constants.sh imagevector charts.yaml Chart

package imagevector

import (
	_ "embed"

	"k8s.io/apimachinery/pkg/util/runtime"

	"github.com/gardener/gardener/pkg/utils/imagevector"
)

var (
	//go:embed containers.yaml
	containersYAML               string
	containersImageVector        imagevector.ImageVector
	containerImagePullSecretName *string

	//go:embed charts.yaml
	chartsYAML               string
	chartsImageVector        imagevector.ImageVector
	chartImagePullSecretName *string
)

func init() {
	var err error

	containersImageVector, containerImagePullSecretName, err = imagevector.Read([]byte(containersYAML))
	runtime.Must(err)
	containersImageVector, containerImagePullSecretName, err = imagevector.WithEnvOverride(containersImageVector, imagevector.OverrideEnv, containerImagePullSecretName)
	runtime.Must(err)

	chartsImageVector, chartImagePullSecretName, err = imagevector.Read([]byte(chartsYAML))
	runtime.Must(err)
	chartsImageVector, chartImagePullSecretName, err = imagevector.WithEnvOverride(chartsImageVector, imagevector.OverrideChartsEnv, chartImagePullSecretName)
	runtime.Must(err)
}

// Containers is the image vector that contains all the needed container images.
func Containers() imagevector.ImageVector {
	return containersImageVector
}

// ContainerImagePullSecretName returns the name of the image pull secret to be used for pulling container images, or nil if no image pull secret is configured.
func ContainerImagePullSecretName() *string {
	return containerImagePullSecretName
}

// Charts is the image vector that contains all the needed Helm chart images.
func Charts() imagevector.ImageVector {
	return chartsImageVector
}

// ChartImagePullSecretName returns the name of the image pull secret to be used for pulling chart images, or nil if no image pull secret is configured.
func ChartImagePullSecretName() *string {
	return chartImagePullSecretName
}
