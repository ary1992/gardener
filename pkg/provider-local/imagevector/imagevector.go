// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

//go:generate ../../../hack/generate-imagename-constants.sh
package imagevector

import (
	_ "embed"

	"k8s.io/apimachinery/pkg/util/runtime"

	"github.com/gardener/gardener/pkg/utils/imagevector"
)

var (
	//go:embed images.yaml
	imagesYAML      string
	imageVector     imagevector.ImageVector
	imagePullSecret *string
)

func init() {
	var err error

	imageVector, imagePullSecret, err = imagevector.Read([]byte(imagesYAML))
	runtime.Must(err)

	imageVector, imagePullSecret, err = imagevector.WithEnvOverride(imageVector, imagevector.OverrideEnv, imagePullSecret)
	runtime.Must(err)
}

// ImageVector is the image vector that contains all the needed images.
func ImageVector() imagevector.ImageVector {
	return imageVector
}

// ImagePullSecretName returns the name of the image pull secret to be used for pulling images, or nil if no image pull secret is configured.
func ImagePullSecretName() *string {
	return imagePullSecret
}
