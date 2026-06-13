package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
)

func TestIsDisconnect(t *testing.T) {
	clean := []error{
		nil,
		io.EOF,
		fmt.Errorf("read: %w", io.EOF),
		net.ErrClosed,
		context.Canceled,
		context.DeadlineExceeded,
	}
	for _, e := range clean {
		if !isDisconnect(e) {
			t.Errorf("isDisconnect(%v) = false, want true (normal stdio end-of-session)", e)
		}
	}
	if isDisconnect(errors.New("genuine transport failure")) {
		t.Error("isDisconnect(arbitrary error) = true, want false")
	}
}
