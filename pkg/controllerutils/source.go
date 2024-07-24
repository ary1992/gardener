// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package controllerutils

import (
	"context"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// EnqueueOnce is a source.Source that simply triggers the reconciler once by directly enqueueing an empty
// reconcile.Request.
var EnqueueOnce = source.Func(func(_ context.Context, q workqueue.RateLimitingInterface) error {
	q.Add(reconcile.Request{})
	return nil
})

// HandleOnce is a source.Source that simply triggers the reconciler once by calling 'Create' at the event handler with
// an empty event.CreateEvent.
var HandleOnce = source.Func(func(ctx context.Context, queue workqueue.RateLimitingInterface) error {
	handler.Create(ctx, event.CreateEvent{}, queue)
	return nil
})

// EnqueueAnonymously is a handler.EventHandler which enqueues a reconcile.Request without any namespace/name data.
var EnqueueAnonymously = handler.TypedEnqueueRequestsFromMapFunc(func(_ context.Context, _ client.Object) []reconcile.Request {
	return []reconcile.Request{{}}
})
