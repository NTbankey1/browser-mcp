// Package proxy copies WebSocket messages between the front door and the
// TypeScript engine worker without parsing them, so the client protocol and
// the extension protocol stay byte-for-byte identical.
package proxy

import (
	"context"
	"errors"
	"time"

	"github.com/coder/websocket"
)

// Pipe runs two message pumps until one side closes or fails, then closes both
// with the same status. The caller must have set a read limit on both sides.
func Pipe(ctx context.Context, left, right *websocket.Conn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, 2)
	go func() { errs <- pump(ctx, right, left) }()
	go func() { errs <- pump(ctx, left, right) }()

	err := <-errs
	cancel()
	closeWith(left, right, err)
	select {
	case <-errs:
	case <-time.After(2 * time.Second):
	}
	return err
}

func pump(ctx context.Context, dst, src *websocket.Conn) error {
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			return err
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			return err
		}
	}
}

func closeWith(left, right *websocket.Conn, err error) {
	code := websocket.StatusNormalClosure
	if !errors.Is(err, context.Canceled) {
		if status := websocket.CloseStatus(err); status != -1 {
			code = status
		}
	}
	_ = left.Close(code, "")
	_ = right.Close(code, "")
}
