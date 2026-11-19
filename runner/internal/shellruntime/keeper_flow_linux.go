//go:build linux

package shellruntime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
)

type keeperOutput struct {
	channel *shellkeeper.Channel
	result  chan bool
}
type bridgeDelivery struct {
	id     string
	frame  RPC
	ctx    context.Context
	cancel context.CancelFunc
}

func outputKey(r RPC) string {
	if r.Version == 2 {
		return r.SessionID + "\x00v2"
	}
	return r.SessionID + "\x00" + r.Shell
}

// A bounded global credit window prevents output from filling the IPC stream.
// A receipt means the upstream delivery has returned, including cancellation.
// Replies use the independent reader and never wait behind a terminal write.
func (k *KeeperRuntime) sendOutput(ctx context.Context, r RPC) error {
	select {
	case k.outputSlots <- struct{}{}:
	case <-ctx.Done():
		return bridgeFailure("output_cancelled", ctx.Err())
	}
	defer func() { <-k.outputSlots }()
	id, err := newHelperID()
	if err != nil {
		return bridgeFailure("output_id", err)
	}
	body, err := json.Marshal(r)
	if err != nil {
		return bridgeFailure("output_encode", err)
	}
	k.mu.Lock()
	ch := k.current
	result := make(chan bool, 1)
	if ch != nil {
		k.outputs[id] = keeperOutput{ch, result}
	}
	k.mu.Unlock()
	if ch == nil {
		return bridgeFailure("output_detached", ErrClosed)
	}
	defer func() { k.mu.Lock(); delete(k.outputs, id); k.mu.Unlock() }()
	if err = ch.SendContext(ctx, bridgePacket{Kind: "output", ID: id, Body: body}); err != nil {
		return err
	}
	select {
	case ok := <-result:
		if ok {
			return nil
		}
		return bridgeFailure("output_delivery", ErrClosed)
	case <-ctx.Done():
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err = ch.SendContext(cleanup, bridgePacket{Kind: "output_cancel", ID: id}); err != nil {
		return err
	}
	select {
	case <-result:
		return bridgeFailure("output_cancelled", ctx.Err())
	case <-cleanup.Done():
		_ = ch.Close()
		return bridgeFailure("output_join_deadline", ErrClosed)
	}
}

func (b *KeeperBridge) deliverOutput(queue <-chan *bridgeDelivery) {
	for {
		select {
		case <-b.done:
			return
		case d := <-queue:
			select {
			case <-b.receiverReady:
			case <-b.done:
				return
			}
			b.mu.Lock()
			receiver := b.receiver
			epoch, blocked := b.blockedOutput[outputKey(d.frame)]
			b.mu.Unlock()
			ok := d.ctx.Err() == nil && (!blocked || d.frame.Epoch > epoch)
			failed := false
			if ok && receiver != nil {
				if err := receiver(d.ctx, d.frame); err != nil {
					_ = bridgeFailure("upstream_delivery", err)
					b.mu.Lock()
					if len(b.blockedOutput) >= 1024 {
						b.mu.Unlock()
						_ = b.Close()
						return
					}
					b.blockedOutput[outputKey(d.frame)] = d.frame.Epoch
					b.mu.Unlock()
					ok = false
					failed = true
				}
			} else {
				ok = false
			}
			d.cancel()
			b.mu.Lock()
			delete(b.deliveries, d.id)
			b.mu.Unlock()
			if b.channel.Send(bridgePacket{Kind: "output_ack", ID: d.id, OK: ok}) != nil {
				_ = b.Close()
				return
			}
			if failed {
				body, _ := json.Marshal(failedOutput{d.frame.SessionID, d.frame.Epoch})
				if b.channel.Send(bridgePacket{Kind: "notify", Method: "lost", Body: body}) != nil {
					_ = b.Close()
					return
				}
			}
		}
	}
}

// NotifyClosed ends browser access on control-channel loss. It is observational:
// it cannot claim physical cleanup or discard the keeper's retained custody.
func (b *KeeperBridge) NotifyClosed(ctx context.Context) {
	b.mu.Lock()
	receiver := b.receiver
	notices := make([]RPC, 0, len(b.notices))
	for _, r := range b.notices {
		r.Data = nil
		r.Ended = true
		r.Offset = r.End
		r.Gap = false
		r.StartCause = "keeper_unavailable"
		notices = append(notices, r)
	}
	b.mu.Unlock()
	if receiver == nil {
		return
	}
	for _, r := range notices {
		if err := receiver(ctx, r); err != nil {
			_ = bridgeFailure("closed_notice", err)
			return
		}
	}
}
