// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package opctx

import (
	"context"
	"time"
)

// WithContext retains operation facilities while replacing cancellation and deadlines.
// It is useful for bounded cleanup after the original operation is cancelled.
func WithContext(ctx Ctx, lifetime context.Context) Ctx {
	return &contextOverride{Ctx: ctx, lifetime: lifetime}
}

//nolint:containedctx // The adapter deliberately replaces only an operation's lifetime.
type contextOverride struct {
	Ctx
	lifetime context.Context
}

func (c *contextOverride) Deadline() (time.Time, bool) { return c.lifetime.Deadline() }
func (c *contextOverride) Done() <-chan struct{}       { return c.lifetime.Done() }
func (c *contextOverride) Err() error {
	//nolint:wrapcheck // context.Context requires the original cancellation sentinel.
	return c.lifetime.Err()
}
func (c *contextOverride) Value(key any) any { return c.lifetime.Value(key) }
