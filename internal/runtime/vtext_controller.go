package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/yusefmosiah/go-choir/internal/store"
)

type pendingVTextWake struct {
	ownerID     string
	docID       string
	parentRunID string
	timer       *time.Timer
}

func vtextWakeKey(ownerID, docID string) string {
	return strings.TrimSpace(ownerID) + "::" + strings.TrimSpace(docID)
}

func (rt *Runtime) scheduleVTextWorkerWake(ownerID, docID, parentRunID string) {
	ownerID = strings.TrimSpace(ownerID)
	docID = strings.TrimSpace(docID)
	if ownerID == "" || docID == "" {
		return
	}
	key := vtextWakeKey(ownerID, docID)
	debounce := rt.cfg.VTextWakeDebounce
	rt.vtextWakeMu.Lock()
	if pending, ok := rt.vtextWakePending[key]; ok && pending.timer != nil {
		pending.timer.Stop()
	}
	timer := time.AfterFunc(debounce, func() {
		rt.flushVTextWorkerWake(key)
	})
	rt.vtextWakePending[key] = pendingVTextWake{
		ownerID:     ownerID,
		docID:       docID,
		parentRunID: strings.TrimSpace(parentRunID),
		timer:       timer,
	}
	rt.vtextWakeMu.Unlock()
}

func (rt *Runtime) flushVTextWorkerWake(key string) {
	rt.vtextWakeMu.Lock()
	pending, ok := rt.vtextWakePending[key]
	if ok {
		delete(rt.vtextWakePending, key)
	}
	rt.vtextWakeMu.Unlock()
	if !ok {
		return
	}
	if err := rt.startPendingVTextWorkerWake(context.Background(), pending); err != nil {
		log.Printf("runtime: debounced vtext wake failed for doc %s: %v", pending.docID, err)
	}
}

func (rt *Runtime) startPendingVTextWorkerWake(ctx context.Context, pending pendingVTextWake) error {
	doc, err := rt.store.GetDocument(ctx, pending.docID, pending.ownerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("load doc for debounced wake: %w", err)
	}
	vtextAgentID := "vtext:" + doc.DocID
	if _, err := rt.store.GetLatestActiveRunByAgent(ctx, pending.ownerID, vtextAgentID); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("check active vtext loop: %w", err)
	}
	if mutation, err := rt.store.GetPendingAgentMutationByDoc(ctx, doc.DocID, pending.ownerID); err == nil && mutation != nil {
		return nil
	} else if err != nil {
		return fmt.Errorf("check pending doc mutation: %w", err)
	}
	_, err = rt.submitVTextAgentRevisionRun(ctx, doc, pending.ownerID, vtextAgentRevisionRequest{
		Intent: "integrate_worker_findings",
	}, pending.parentRunID)
	if err != nil {
		return fmt.Errorf("start debounced vtext revision: %w", err)
	}
	return nil
}
