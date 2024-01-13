package tesla_api

import (
	"context"

	"github.com/cenkalti/backoff/v4"
)

func infiniteExponentialBackoff(ctx context.Context) backoff.BackOffContext {
	b := backoff.NewExponentialBackOff()
	b.MaxElapsedTime = 0
	return backoff.WithContext(b, ctx)
}
