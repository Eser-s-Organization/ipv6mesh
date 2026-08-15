package control

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Event struct {
	Type     string
	Snapshot *NetworkSnapshot
}

func DecodeEvent(data []byte) (Event, error) {
	var envelope struct {
		Type     string                `json:"type"`
		Snapshot *snapshotWireResponse `json:"snapshot"`
	}
	if err := decodeLimitedJSONBytes(data, &envelope); err != nil {
		return Event{}, err
	}
	if envelope.Type != "snapshot" || envelope.Snapshot == nil {
		return Event{}, fmt.Errorf("%w: unsupported event type", ErrControlInvalidResponse)
	}
	snapshot, err := snapshotFromWire(*envelope.Snapshot)
	if err != nil {
		return Event{}, err
	}
	return Event{Type: envelope.Type, Snapshot: &snapshot}, nil
}

type WatchOptions struct {
	InitialBackoff  time.Duration
	MaxBackoff      time.Duration
	MaxMessageBytes int64
}

func (client *Client) Watch(ctx context.Context, networkID, sessionToken string, onSnapshot func(NetworkSnapshot) error) error {
	return client.WatchWithOptions(ctx, networkID, sessionToken, onSnapshot, WatchOptions{})
}

func (client *Client) WatchWithOptions(ctx context.Context, networkID, sessionToken string, onSnapshot func(NetworkSnapshot) error, options WatchOptions) error {
	if onSnapshot == nil {
		return ErrInvalidClient
	}
	if options.InitialBackoff <= 0 {
		options.InitialBackoff = 250 * time.Millisecond
	}
	if options.MaxBackoff < options.InitialBackoff {
		options.MaxBackoff = 10 * time.Second
	}
	if options.MaxMessageBytes <= 0 {
		options.MaxMessageBytes = defaultControlResponseLimit
	}
	backoff := options.InitialBackoff
	for {
		if err := contextError(ctx); err != nil {
			return err
		}
		connection, err := client.Events(ctx, networkID, sessionToken)
		if err != nil {
			if !watchRetryable(err) {
				return err
			}
			if err := waitBackoff(ctx, backoff); err != nil {
				return err
			}
			backoff = nextBackoff(backoff, options.MaxBackoff)
			continue
		}
		connection.SetReadLimit(options.MaxMessageBytes)
		received := false
		for {
			if err := contextError(ctx); err != nil {
				_ = connection.Close()
				return err
			}
			_, message, readErr := connection.ReadMessage()
			if readErr != nil {
				_ = connection.Close()
				break
			}
			event, decodeErr := DecodeEvent(message)
			if decodeErr != nil {
				_ = connection.Close()
				return decodeErr
			}
			if event.Snapshot == nil || event.Snapshot.NetworkID != networkID {
				_ = connection.Close()
				return fmt.Errorf("%w: event network does not match requested network", ErrControlInvalidResponse)
			}
			if err := onSnapshot(*event.Snapshot); err != nil {
				_ = connection.Close()
				return err
			}
			received = true
			backoff = options.InitialBackoff
		}
		if err := contextError(ctx); err != nil {
			return err
		}
		if !received {
			backoff = nextBackoff(backoff, options.MaxBackoff)
		}
		if err := waitBackoff(ctx, backoff); err != nil {
			return err
		}
	}
}

func watchRetryable(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case 401, 403, 404, 409:
			return false
		}
	}
	return true
}

func waitBackoff(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum/2 || current > maximum-current {
		return maximum
	}
	return current * 2
}

func decodeLimitedJSONBytes(data []byte, destination any) error {
	if len(data) == 0 {
		return fmt.Errorf("%w: empty event", ErrControlInvalidResponse)
	}
	return decodeStrictJSONBytes(data, destination)
}
