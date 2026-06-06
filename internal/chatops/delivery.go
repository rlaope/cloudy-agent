package chatops

import (
	"context"
	"fmt"
	"net/http"
)

// MultiDelivery dispatches by target platform to configured delivery clients.
type MultiDelivery map[string]Delivery

func (m MultiDelivery) Deliver(ctx context.Context, target ReplyTarget, msg Message) error {
	if m == nil {
		return nil
	}
	d := m[target.Platform]
	if d == nil {
		return nil
	}
	return d.Deliver(ctx, target, msg)
}

func doChatOpsRequest(client *http.Client, req *http.Request, label string) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: request failed", label)
	}
	return resp, nil
}
