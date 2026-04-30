package adapter

import (
	"context"
	"time"
)

type ImageGenerateObserver interface {
	RecordSubmitDuration(time.Duration)
	RecordPollDuration(time.Duration)
	RecordUpstreamRequestID(string)
	RecordDownstreamStatus(string)
}

type imageGenerateObserverKey struct{}

func WithImageGenerateObserver(ctx context.Context, observer ImageGenerateObserver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, imageGenerateObserverKey{}, observer)
}

func imageGenerateObserverFromContext(ctx context.Context) ImageGenerateObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(imageGenerateObserverKey{}).(ImageGenerateObserver)
	return observer
}
