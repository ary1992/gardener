/*
Copyright (c) 2020 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file

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

// Code generated by client-gen. DO NOT EDIT.

package internalversion

import (
	"context"
	"time"

	core "github.com/gardener/gardener/pkg/apis/core"
	scheme "github.com/gardener/gardener/pkg/client/core/clientset/internalversion/scheme"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	rest "k8s.io/client-go/rest"
)

// CloudProfilesGetter has a method to return a CloudProfileInterface.
// A group's client should implement this interface.
type CloudProfilesGetter interface {
	CloudProfiles() CloudProfileInterface
}

// CloudProfileInterface has methods to work with CloudProfile resources.
type CloudProfileInterface interface {
	Create(ctx context.Context, cloudProfile *core.CloudProfile, opts v1.CreateOptions) (*core.CloudProfile, error)
	Update(ctx context.Context, cloudProfile *core.CloudProfile, opts v1.UpdateOptions) (*core.CloudProfile, error)
	Delete(ctx context.Context, name string, opts v1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error
	Get(ctx context.Context, name string, opts v1.GetOptions) (*core.CloudProfile, error)
	List(ctx context.Context, opts v1.ListOptions) (*core.CloudProfileList, error)
	Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *core.CloudProfile, err error)
	CloudProfileExpansion
}

// cloudProfiles implements CloudProfileInterface
type cloudProfiles struct {
	client rest.Interface
}

// newCloudProfiles returns a CloudProfiles
func newCloudProfiles(c *CoreClient) *cloudProfiles {
	return &cloudProfiles{
		client: c.RESTClient(),
	}
}

// Get takes name of the cloudProfile, and returns the corresponding cloudProfile object, and an error if there is any.
func (c *cloudProfiles) Get(ctx context.Context, name string, options v1.GetOptions) (result *core.CloudProfile, err error) {
	result = &core.CloudProfile{}
	err = c.client.Get().
		Resource("cloudprofiles").
		Name(name).
		VersionedParams(&options, scheme.ParameterCodec).
		Do(ctx).
		Into(result)
	return
}

// List takes label and field selectors, and returns the list of CloudProfiles that match those selectors.
func (c *cloudProfiles) List(ctx context.Context, opts v1.ListOptions) (result *core.CloudProfileList, err error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	result = &core.CloudProfileList{}
	err = c.client.Get().
		Resource("cloudprofiles").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Do(ctx).
		Into(result)
	return
}

// Watch returns a watch.Interface that watches the requested cloudProfiles.
func (c *cloudProfiles) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	opts.Watch = true
	return c.client.Get().
		Resource("cloudprofiles").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Watch(ctx)
}

// Create takes the representation of a cloudProfile and creates it.  Returns the server's representation of the cloudProfile, and an error, if there is any.
func (c *cloudProfiles) Create(ctx context.Context, cloudProfile *core.CloudProfile, opts v1.CreateOptions) (result *core.CloudProfile, err error) {
	result = &core.CloudProfile{}
	err = c.client.Post().
		Resource("cloudprofiles").
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(cloudProfile).
		Do(ctx).
		Into(result)
	return
}

// Update takes the representation of a cloudProfile and updates it. Returns the server's representation of the cloudProfile, and an error, if there is any.
func (c *cloudProfiles) Update(ctx context.Context, cloudProfile *core.CloudProfile, opts v1.UpdateOptions) (result *core.CloudProfile, err error) {
	result = &core.CloudProfile{}
	err = c.client.Put().
		Resource("cloudprofiles").
		Name(cloudProfile.Name).
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(cloudProfile).
		Do(ctx).
		Into(result)
	return
}

// Delete takes name of the cloudProfile and deletes it. Returns an error if one occurs.
func (c *cloudProfiles) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	return c.client.Delete().
		Resource("cloudprofiles").
		Name(name).
		Body(&opts).
		Do(ctx).
		Error()
}

// DeleteCollection deletes a collection of objects.
func (c *cloudProfiles) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	var timeout time.Duration
	if listOpts.TimeoutSeconds != nil {
		timeout = time.Duration(*listOpts.TimeoutSeconds) * time.Second
	}
	return c.client.Delete().
		Resource("cloudprofiles").
		VersionedParams(&listOpts, scheme.ParameterCodec).
		Timeout(timeout).
		Body(&opts).
		Do(ctx).
		Error()
}

// Patch applies the patch and returns the patched cloudProfile.
func (c *cloudProfiles) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *core.CloudProfile, err error) {
	result = &core.CloudProfile{}
	err = c.client.Patch(pt).
		Resource("cloudprofiles").
		Name(name).
		SubResource(subresources...).
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(data).
		Do(ctx).
		Into(result)
	return
}
