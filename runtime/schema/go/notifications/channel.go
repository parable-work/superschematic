package notifications

import "context"

// channelKey is the context key WithChannel stores a channel kind under.
type channelKey struct{}

// WithChannel returns ctx whose Host.Acting runs its behaviors' commands
// as a caller that came over channel (D157, #606): the channel kind of a
// reply the notifier runs as its sender, which Comments records as a
// message's via. "" names none.
func WithChannel(ctx context.Context, channel string) context.Context {
	return context.WithValue(ctx, channelKey{}, channel)
}

// ChannelOf is the channel kind WithChannel gave ctx; "" for none.
func ChannelOf(ctx context.Context) string {
	channel, _ := ctx.Value(channelKey{}).(string)
	return channel
}
