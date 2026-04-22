package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

type pendingVTextWake struct {
	ownerID string
	docID   string
	timer   *time.Timer
}

func vtextWakeKey(ownerID, docID string) string {
	return strings.TrimSpace(ownerID) + "::" + strings.TrimSpace(docID)
}

func (rt *Runtime) scheduleVTextWorkerWake(ownerID, docID, _ string) {
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
		ownerID: ownerID,
		docID:   docID,
		timer:   timer,
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
	if err := rt.reconcileVTextWorkerState(context.Background(), pending.ownerID, pending.docID); err != nil {
		log.Printf("runtime: reconcile vtext wake failed for doc %s: %v", pending.docID, err)
	}
}

func (rt *Runtime) reconcileAllVTextDocuments(ctx context.Context) {
	docs, err := rt.store.ListAllDocuments(ctx, 2000)
	if err != nil {
		log.Printf("runtime: reconcile all vtext docs: %v", err)
		return
	}
	for _, doc := range docs {
		if err := rt.reconcileVTextWorkerState(ctx, doc.OwnerID, doc.DocID); err != nil {
			log.Printf("runtime: reconcile doc %s: %v", doc.DocID, err)
		}
	}
}

// reconcileVTextWorkerState is the durable controller invariant for vtext:
// if worker messages newer than the integrated checkpoint exist, and no synth
// run is active or pending, launch exactly one new synth run.
func (rt *Runtime) reconcileVTextWorkerState(ctx context.Context, ownerID, docID string) error {
	ownerID = strings.TrimSpace(ownerID)
	docID = strings.TrimSpace(docID)
	if ownerID == "" || docID == "" {
		return nil
	}
	doc, err := rt.store.GetDocument(ctx, docID, ownerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("load doc for reconcile: %w", err)
	}
	checkpoint, err := rt.store.GetVTextControllerCheckpoint(ctx, doc.DocID, ownerID)
	if err != nil {
		return fmt.Errorf("load controller checkpoint: %w", err)
	}
	integratedSeq := int64(0)
	if checkpoint != nil {
		integratedSeq = checkpoint.IntegratedMessageSeq
	}
	latestMessage, found, err := rt.latestEligibleWorkerMessage(ctx, ownerID, doc.DocID, integratedSeq)
	if err != nil {
		return fmt.Errorf("latest eligible worker message: %w", err)
	}
	if !found {
		return nil
	}
	vtextAgentID := "vtext:" + doc.DocID
	if _, err := rt.store.GetLatestActiveRunByAgent(ctx, ownerID, vtextAgentID); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("check active vtext loop: %w", err)
	}
	if mutation, err := rt.store.GetPendingAgentMutationByDoc(ctx, doc.DocID, ownerID); err == nil && mutation != nil {
		return nil
	} else if err != nil {
		return fmt.Errorf("check pending doc mutation: %w", err)
	}
	_, err = rt.submitVTextAgentRevisionRun(ctx, doc, ownerID, vtextAgentRevisionRequest{
		Intent: "integrate_worker_findings",
	}, latestMessage.FromRunID, latestMessage.Seq)
	if err != nil {
		return fmt.Errorf("start reconciled vtext revision: %w", err)
	}
	return nil
}

func (rt *Runtime) latestEligibleWorkerMessage(ctx context.Context, ownerID, channelID string, afterSeq int64) (types.ChannelMessage, bool, error) {
	const batchSize = 200
	cache := make(map[string]bool)
	cursor := afterSeq
	var latest types.ChannelMessage
	found := false
	for {
		messages, err := rt.store.ListChannelMessages(ctx, ownerID, channelID, cursor, batchSize)
		if err != nil {
			return types.ChannelMessage{}, false, err
		}
		if len(messages) == 0 {
			break
		}
		for _, message := range messages {
			if message.Seq > cursor {
				cursor = message.Seq
			}
			ok, err := rt.isEligibleWorkerMessage(ctx, channelID, message, cache)
			if err != nil {
				return types.ChannelMessage{}, false, err
			}
			if !ok {
				continue
			}
			latest = message
			found = true
		}
		if len(messages) < batchSize {
			break
		}
	}
	return latest, found, nil
}

func (rt *Runtime) isEligibleWorkerMessage(ctx context.Context, docID string, message types.ChannelMessage, cache map[string]bool) (bool, error) {
	if strings.TrimSpace(message.ToAgentID) != "vtext:"+strings.TrimSpace(docID) {
		return false, nil
	}
	runID := strings.TrimSpace(message.FromRunID)
	if runID == "" {
		return false, nil
	}
	if cached, ok := cache[runID]; ok {
		return cached, nil
	}
	run, err := rt.store.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			cache[runID] = false
			return false, nil
		}
		return false, err
	}
	switch agentProfileForRun(&run) {
	case AgentProfileResearcher, AgentProfileSuper, AgentProfileCoSuper:
		cache[runID] = true
		return true, nil
	default:
		cache[runID] = false
		return false, nil
	}
}
