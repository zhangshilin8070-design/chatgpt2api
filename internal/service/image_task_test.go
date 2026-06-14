package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt2api/internal/storage"
	"chatgpt2api/internal/util"
)

func TestImageTaskServiceIdempotencyOwnerIsolationAndCompletion(t *testing.T) {
	handlerCalls := make(chan map[string]any, 4)
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		handlerCalls <- payload
		return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })

	alice := Identity{ID: "alice", Name: "Alice", Role: "user"}
	bob := Identity{ID: "bob", Name: "Bob", Role: "user"}

	first, err := svc.SubmitGeneration(context.Background(), alice, "task-1", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil)
	if err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	second, err := svc.SubmitGeneration(context.Background(), alice, "task-1", "different", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil)
	if err != nil {
		t.Fatalf("second SubmitGeneration() error = %v", err)
	}
	if first["id"] != second["id"] {
		t.Fatalf("idempotent task id mismatch: %#v %#v", first, second)
	}
	waitForTaskStatus(t, svc, alice, "task-1", TaskStatusSuccess)
	select {
	case <-handlerCalls:
	default:
		t.Fatal("handler was not called")
	}
	if len(handlerCalls) != 0 {
		t.Fatalf("handler calls after duplicate = %d extra, want 0", len(handlerCalls))
	}
	if got := svc.ListTasks(bob, []string{"task-1"}); len(got["items"].([]map[string]any)) != 0 {
		t.Fatalf("bob can see alice task: %#v", got)
	}
	if got := svc.ListTasks(bob, []string{"task-1"}); len(got["missing_ids"].([]string)) != 1 {
		t.Fatalf("bob missing ids = %#v", got)
	}
}

func TestImageTaskServiceUsesOwnerIDAroundCredentialRotation(t *testing.T) {
	handlerCalls := make(chan map[string]any, 4)
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		handlerCalls <- payload
		return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })
	ownerID := "linuxdo:123"
	oldKey := Identity{ID: ownerID, OwnerID: ownerID, CredentialID: "key-old", Name: "Alice", Role: "user"}
	newKey := Identity{ID: ownerID, OwnerID: ownerID, CredentialID: "key-new", Name: "Alice", Role: "user"}
	otherOwner := Identity{ID: "linuxdo:456", OwnerID: "linuxdo:456", CredentialID: "key-other", Name: "Bob", Role: "user"}

	if _, err := svc.SubmitGeneration(context.Background(), oldKey, "task-1", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	waitForTaskStatus(t, svc, newKey, "task-1", TaskStatusSuccess)
	if got := svc.ListTasks(newKey, []string{"task-1"}); len(got["items"].([]map[string]any)) != 1 {
		t.Fatalf("rotated credential cannot see owner task: %#v", got)
	}
	if got := svc.ListTasks(otherOwner, []string{"task-1"}); len(got["items"].([]map[string]any)) != 0 || len(got["missing_ids"].([]string)) != 1 {
		t.Fatalf("other owner should not see task: %#v", got)
	}
	if _, err := svc.SubmitGeneration(context.Background(), newKey, "task-1", "different", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("second SubmitGeneration() error = %v", err)
	}
	if len(handlerCalls) != 1 {
		t.Fatalf("credential rotation should not create a duplicate task, handler calls = %d", len(handlerCalls))
	}
}

func TestImageTaskServiceListTasksReturnsEmptyArrays(t *testing.T) {
	svc := newTestImageTaskService(t, failingImageTaskHandler, failingImageTaskHandler, failingImageTaskHandler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}

	for name, got := range map[string]map[string]any{
		"empty list":   svc.ListTasks(identity, nil),
		"missing task": svc.ListTasks(identity, []string{"missing"}),
	} {
		items, ok := got["items"].([]map[string]any)
		if !ok {
			t.Fatalf("%s items type = %T", name, got["items"])
		}
		if items == nil {
			t.Fatalf("%s items is nil", name)
		}
		missing, ok := got["missing_ids"].([]string)
		if !ok {
			t.Fatalf("%s missing_ids type = %T", name, got["missing_ids"])
		}
		if missing == nil {
			t.Fatalf("%s missing_ids is nil", name)
		}

		data, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s Marshal() error = %v", name, err)
		}
		text := string(data)
		if strings.Contains(text, `"items":null`) || strings.Contains(text, `"missing_ids":null`) {
			t.Fatalf("%s encoded nil arrays: %s", name, text)
		}
	}
}

func TestImageTaskServiceRejectsBlankPromptBeforeQueueing(t *testing.T) {
	svc := newTestImageTaskService(t, failingImageTaskHandler, failingImageTaskHandler, failingImageTaskHandler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}

	for name, submit := range map[string]func() (map[string]any, error){
		"generation": func() (map[string]any, error) {
			return svc.SubmitGeneration(context.Background(), identity, "task-1", "  ", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil)
		},
		"edit": func() (map[string]any, error) {
			return svc.SubmitEdit(context.Background(), identity, "task-2", "\t", "gpt-image-2", "1024x1024", "high", "https://base.test", []any{"image"}, 1, nil)
		},
		"chat": func() (map[string]any, error) {
			return svc.SubmitChat(context.Background(), identity, "task-3", " ", "auto", []map[string]any{{"role": "user", "content": "hello"}}, false)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := submit(); err == nil || err.Error() != "prompt is required" {
				t.Fatalf("Submit() error = %v, want prompt is required", err)
			}
		})
	}

	got := svc.ListTasks(identity, nil)
	if len(got["items"].([]map[string]any)) != 0 {
		t.Fatalf("blank prompt should not queue tasks: %#v", got)
	}
}

func TestImageTaskServicePassesMessagesToHandler(t *testing.T) {
	handlerCalls := make(chan map[string]any, 1)
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		handlerCalls <- payload
		return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}
	messages := []any{
		map[string]any{"role": "user", "content": "你好，你是什么模型？"},
		map[string]any{"role": "assistant", "content": "我是 GPT-5 Mini。"},
		map[string]any{"role": "user", "content": "我之前说了什么？"},
	}

	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-1", "我之前说了什么？", "auto", "", "high", "https://base.test", 1, messages); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}

	var payload map[string]any
	select {
	case payload = <-handlerCalls:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler payload")
	}
	if got := payload["messages"]; got == nil {
		t.Fatalf("payload messages missing: %#v", payload)
	}
	if got := payload["prompt"]; got != "我之前说了什么？" {
		t.Fatalf("payload prompt = %#v, want current prompt", got)
	}
	if got := payload["quality"]; got != "high" {
		t.Fatalf("payload quality = %#v, want high", got)
	}
	waitForTaskStatus(t, svc, identity, "task-1", TaskStatusSuccess)
}

func TestImageTaskServicePassesImageRequestMetadataToHandler(t *testing.T) {
	handlerCalls := make(chan map[string]any, 1)
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		handlerCalls <- payload
		return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}

	if _, err := svc.SubmitGenerationWithMetadata(context.Background(), identity, "task-1", "draw", "gpt-image-2", "2048x2048", "high", "https://base.test", 1, nil, map[string]any{"image_resolution": "2k", "requested_size": "2048x2048"}); err != nil {
		t.Fatalf("SubmitGenerationWithMetadata() error = %v", err)
	}

	select {
	case payload := <-handlerCalls:
		if got := payload["image_resolution"]; got != "2k" {
			t.Fatalf("payload image_resolution = %#v, want 2k in %#v", got, payload)
		}
		if got := payload["requested_size"]; got != "2048x2048" {
			t.Fatalf("payload requested_size = %#v, want 2048x2048 in %#v", got, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler payload")
	}
	waitForTaskStatus(t, svc, identity, "task-1", TaskStatusSuccess)
}

func TestImageTaskServicePassesImageToolOptionsToHandler(t *testing.T) {
	handlerCalls := make(chan map[string]any, 1)
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		handlerCalls <- payload
		return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}
	partialImages := 2

	if _, err := svc.SubmitGenerationWithOptions(context.Background(), identity, "task-1", "draw", "gpt-image-2", "16:9", "high", "https://base.test", 1, nil, nil, ImageOutputOptions{Format: "webp"}, ImageToolOptions{Background: "transparent", Moderation: "auto", Style: "vivid", PartialImages: &partialImages}); err != nil {
		t.Fatalf("SubmitGenerationWithOptions() error = %v", err)
	}

	select {
	case payload := <-handlerCalls:
		for key, want := range map[string]any{"background": "transparent", "moderation": "auto", "style": "vivid", "partial_images": 2, "output_format": "webp"} {
			if got := payload[key]; got != want {
				t.Fatalf("payload[%s] = %#v, want %#v in %#v", key, got, want, payload)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler payload")
	}
	waitForTaskStatus(t, svc, identity, "task-1", TaskStatusSuccess)
}

func TestImageTaskServiceSubmitsChatTasks(t *testing.T) {
	handlerCalls := make(chan map[string]any, 1)
	imageHandler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
	}
	chatHandler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		handlerCalls <- payload
		return map[string]any{"output_type": "text", "data": []map[string]any{{"text_response": "chat response"}}}, nil
	}
	svc := newTestImageTaskService(t, imageHandler, imageHandler, chatHandler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}
	messages := []map[string]any{{"role": "user", "content": "hello"}}

	if _, err := svc.SubmitChat(context.Background(), identity, "chat-1", "hello", "auto", messages, false); err != nil {
		t.Fatalf("SubmitChat() error = %v", err)
	}
	waitForTaskStatus(t, svc, identity, "chat-1", TaskStatusSuccess)
	got := svc.ListTasks(identity, []string{"chat-1"})
	item := got["items"].([]map[string]any)[0]
	if item["mode"] != "chat" {
		t.Fatalf("mode = %#v, want chat in %#v", item["mode"], item)
	}
	if item["output_type"] != "text" {
		t.Fatalf("output_type = %#v, want text in %#v", item["output_type"], item)
	}
	data := item["data"].([]map[string]any)
	if len(data) != 1 || data[0]["text_response"] != "chat response" {
		t.Fatalf("text response data = %#v", data)
	}
	select {
	case payload := <-handlerCalls:
		if got := payload["messages"]; got == nil {
			t.Fatalf("chat payload messages missing: %#v", payload)
		}
	default:
		t.Fatal("chat handler was not called")
	}
}

func TestImageTaskServiceDoesNotLimitGlobalImageSlots(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		started <- payload["prompt"].(string)
		<-release
		return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}

	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-1", "first", "gpt-image-2", "1024x1024", "high", "https://base.test", 4, nil); err != nil {
		t.Fatalf("SubmitGeneration(first) error = %v", err)
	}
	if got := waitForStartedTask(t, started); got != "first" {
		t.Fatalf("started task = %q, want first", got)
	}
	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-2", "second", "gpt-image-2", "1024x1024", "high", "https://base.test", 4, nil); err != nil {
		t.Fatalf("SubmitGeneration(second) error = %v", err)
	}
	if got := waitForStartedTask(t, started); got != "second" {
		t.Fatalf("second task should not wait for global image slots, started = %q", got)
	}
	close(release)
	waitForTaskStatus(t, svc, identity, "task-1", TaskStatusSuccess)
	waitForTaskStatus(t, svc, identity, "task-2", TaskStatusSuccess)
}

func TestImageTaskServicePublishesPartialImageDataWhileRunning(t *testing.T) {
	partialPublished := make(chan struct{})
	release := make(chan struct{})
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		callback, ok := payload[imageOutputCallbackPayloadKey].(func([]map[string]any))
		if !ok {
			return nil, errors.New("image output callback missing")
		}
		callback([]map[string]any{
			{},
			{"url": "https://example.test/second.png"},
		})
		close(partialPublished)
		<-release
		return map[string]any{"data": []map[string]any{
			{"url": "https://example.test/first.png"},
			{"url": "https://example.test/second.png"},
		}}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}

	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-1", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 2, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	select {
	case <-partialPublished:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for partial task data")
	}
	waitForTaskData(t, svc, identity, "task-1", func(data []map[string]any) bool {
		return len(data) == 2 && len(data[0]) == 0 && data[1]["url"] == "https://example.test/second.png"
	})
	close(release)
	waitForTaskStatus(t, svc, identity, "task-1", TaskStatusSuccess)
}

func TestImageTaskServiceLimitsUserDefaultConcurrentCreationUnits(t *testing.T) {
	startedImages := make(chan int, 3)
	release := make(chan struct{})
	var mu sync.Mutex
	activeImages := 0
	maxActiveImages := 0
	imageHandler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		acquire, ok := payload["image_output_slot_acquirer"].(func(context.Context, int) (func(), error))
		if !ok {
			return nil, errors.New("image output slot acquirer missing")
		}
		count := imageTaskCount(payload)
		errCh := make(chan error, count)
		var wg sync.WaitGroup
		for index := 1; index <= count; index++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				releaseSlot, err := acquire(ctx, index)
				if err != nil {
					errCh <- err
					return
				}
				defer releaseSlot()
				mu.Lock()
				activeImages++
				if activeImages > maxActiveImages {
					maxActiveImages = activeImages
				}
				mu.Unlock()
				startedImages <- index
				select {
				case <-release:
				case <-ctx.Done():
					errCh <- ctx.Err()
				}
				mu.Lock()
				activeImages--
				mu.Unlock()
			}(index)
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			if err != nil {
				return nil, err
			}
		}
		data := make([]map[string]any, 0, count)
		for index := 1; index <= count; index++ {
			data = append(data, map[string]any{"url": "https://example.test/image.png"})
		}
		return map[string]any{"data": data}, nil
	}
	chatHandler := func(context.Context, Identity, map[string]any) (map[string]any, error) {
		return map[string]any{"output_type": "text", "data": []map[string]any{{"text_response": "chat response"}}}, nil
	}
	svc := newTestImageTaskService(t, imageHandler, imageHandler, chatHandler, func() int { return 30 }, func() int { return 2 })
	alice := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}

	if _, err := svc.SubmitGeneration(context.Background(), alice, "task-1", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 3, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	seen := map[int]bool{}
	seen[waitForStartedImageIndex(t, startedImages)] = true
	seen[waitForStartedImageIndex(t, startedImages)] = true
	if len(seen) != 2 {
		t.Fatalf("started image indexes = %#v, want two distinct images", seen)
	}
	select {
	case index := <-startedImages:
		t.Fatalf("third image output started before a user slot was released: %d", index)
	case <-time.After(120 * time.Millisecond):
	}
	mu.Lock()
	gotMaxActive := maxActiveImages
	mu.Unlock()
	if gotMaxActive != 2 {
		t.Fatalf("max active image outputs = %d, want 2", gotMaxActive)
	}
	waitForTaskStatus(t, svc, alice, "task-1", TaskStatusRunning)
	waitForTaskOutputStatusCounts(t, svc, alice, "task-1", map[string]int{"running": 2, "queued": 1})
	close(release)
	seen[waitForStartedImageIndex(t, startedImages)] = true
	waitForTaskStatus(t, svc, alice, "task-1", TaskStatusSuccess)
	if len(seen) != 3 {
		t.Fatalf("started image indexes after release = %#v, want three images", seen)
	}
	started := make(chan string, 3)
	releaseImage := make(chan struct{})
	releaseChat := make(chan struct{})
	imageHandler = func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		acquire, ok := payload["image_output_slot_acquirer"].(func(context.Context, int) (func(), error))
		if !ok {
			return nil, errors.New("image output slot acquirer missing")
		}
		count := imageTaskCount(payload)
		errCh := make(chan error, count)
		var wg sync.WaitGroup
		for index := 1; index <= count; index++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				releaseSlot, err := acquire(ctx, index)
				if err != nil {
					errCh <- err
					return
				}
				defer releaseSlot()
				started <- "image"
				select {
				case <-releaseImage:
				case <-ctx.Done():
					errCh <- ctx.Err()
				}
			}(index)
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			if err != nil {
				return nil, err
			}
		}
		return map[string]any{"data": []map[string]any{{"url": "https://example.test/first.png"}, {"url": "https://example.test/second.png"}}}, nil
	}
	chatHandler = func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		started <- "chat"
		select {
		case <-releaseChat:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return map[string]any{"output_type": "text", "data": []map[string]any{{"text_response": "chat response"}}}, nil
	}
	svc = newTestImageTaskService(t, imageHandler, imageHandler, chatHandler, func() int { return 30 }, func() int { return 2 })
	messages := []map[string]any{{"role": "user", "content": "hello"}}

	if _, err := svc.SubmitEdit(context.Background(), alice, "edit-1", "edit", "gpt-image-2", "1024x1024", "high", "https://base.test", []any{"image"}, 2, nil); err != nil {
		t.Fatalf("SubmitEdit(edit-1) error = %v", err)
	}
	if got := waitForStartedTask(t, started); got != "image" {
		t.Fatalf("started task = %q, want image", got)
	}
	if got := waitForStartedTask(t, started); got != "image" {
		t.Fatalf("started task = %q, want image", got)
	}
	if _, err := svc.SubmitChat(context.Background(), alice, "chat-1", "hello", "auto", messages, false); err != nil {
		t.Fatalf("SubmitChat(chat-1) error = %v", err)
	}
	waitForTaskStatus(t, svc, alice, "chat-1", TaskStatusQueued)
	select {
	case item := <-started:
		t.Fatalf("chat task started before an image slot was released: %s", item)
	case <-time.After(120 * time.Millisecond):
	}
	close(releaseImage)
	if got := waitForStartedTask(t, started); got != "chat" {
		t.Fatalf("started task = %q, want chat", got)
	}
	waitForTaskStatus(t, svc, alice, "chat-1", TaskStatusRunning)
	close(releaseChat)
	waitForTaskStatus(t, svc, alice, "edit-1", TaskStatusSuccess)
	waitForTaskStatus(t, svc, alice, "chat-1", TaskStatusSuccess)
}

func TestImageTaskServiceLimitsUserDefaultRPM(t *testing.T) {
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 }, nil, func() int { return 1 })
	user := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}
	admin := Identity{ID: "admin", Name: "Admin", Role: AuthRoleAdmin}

	if _, err := svc.SubmitGeneration(context.Background(), user, "task-1", "first", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("SubmitGeneration(first) error = %v", err)
	}
	waitForTaskStatus(t, svc, user, "task-1", TaskStatusSuccess)
	if _, err := svc.SubmitGeneration(context.Background(), user, "task-2", "second", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil); err == nil {
		t.Fatal("SubmitGeneration(second) error = nil, want RPM limit")
	} else {
		var limitErr ImageTaskLimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("SubmitGeneration(second) error = %T %v, want ImageTaskLimitError", err, err)
		}
	}
	if _, err := svc.SubmitGeneration(context.Background(), admin, "task-1", "admin first", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("admin should bypass user RPM limit: %v", err)
	}
	if _, err := svc.SubmitGeneration(context.Background(), admin, "task-2", "admin second", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("admin should bypass user RPM limit on second request: %v", err)
	}
	waitForTaskStatus(t, svc, admin, "task-1", TaskStatusSuccess)
	waitForTaskStatus(t, svc, admin, "task-2", TaskStatusSuccess)
}

func TestImageTaskServiceCancelsRunningTask(t *testing.T) {
	started := make(chan struct{})
	handlerDone := make(chan error, 1)
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		close(started)
		<-ctx.Done()
		handlerDone <- ctx.Err()
		return nil, ctx.Err()
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}

	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-1", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task handler to start")
	}

	cancelled, err := svc.CancelTask(identity, "task-1")
	if err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
	if cancelled["status"] != TaskStatusCancelled {
		t.Fatalf("cancelled task status = %#v", cancelled)
	}
	select {
	case err := <-handlerDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler ctx err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("task handler did not observe cancellation")
	}
	waitForTaskStatus(t, svc, identity, "task-1", TaskStatusCancelled)
}

func TestImageTaskServicePreservesPartialDataOnFailure(t *testing.T) {
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		return map[string]any{"data": []map[string]any{{"url": "https://example.test/first.png"}}}, errors.New("second image failed")
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}

	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-1", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 2, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	waitForTaskStatus(t, svc, identity, "task-1", TaskStatusError)
	got := svc.ListTasks(identity, []string{"task-1"})
	item := got["items"].([]map[string]any)[0]
	data := item["data"].([]map[string]any)
	if len(data) != 1 || data[0]["url"] != "https://example.test/first.png" {
		t.Fatalf("partial data was not preserved: %#v", item)
	}
	if item["error"] != "second image failed" {
		t.Fatalf("partial failure error = %#v", item)
	}
	statuses := util.AsStringSlice(item["output_statuses"])
	if len(statuses) != 2 || statuses[0] != "success" || statuses[1] != "error" {
		t.Fatalf("output_statuses = %#v, want partial success and failed remainder", statuses)
	}
}

func TestImageTaskServiceBillingSuccessFailureCancelAndTextOutput(t *testing.T) {
	operator := Identity{ID: "admin", Name: "Admin", Role: AuthRoleAdmin}
	user := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}
	newBilling := func(t *testing.T, defaults testBillingDefaults) *BillingService {
		t.Helper()
		billing := newTestBillingService(t, defaults)
		billing.InitializeUserDefaults("alice")
		return billing
	}

	t.Run("partial success consumes actual outputs", func(t *testing.T) {
		svc := newTestImageTaskService(t,
			func(context.Context, Identity, map[string]any) (map[string]any, error) {
				return map[string]any{"data": []map[string]any{
					{"url": "https://example.test/first.png"},
					{"url": "https://example.test/second.png"},
				}}, nil
			},
			failingImageTaskHandler,
			failingImageTaskHandler,
			func() int { return 30 },
		)
		billing := newBilling(t, testBillingDefaults{standardBalance: 4})
		svc.SetBillingService(billing)
		if _, err := svc.SubmitGeneration(context.Background(), user, "success", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 4, nil); err != nil {
			t.Fatalf("SubmitGeneration() error = %v", err)
		}
		waitForTaskStatus(t, svc, user, "success", TaskStatusSuccess)
		got := svc.ListTasks(user, []string{"success"})
		item := got["items"].([]map[string]any)[0]
		if util.ToInt(item["billing_consumed_amount"], -1) != 2 {
			t.Fatalf("task billing = %#v", item)
		}
		state := billing.Get("alice")
		bucket := bucketA(t, state)
		standard := util.StringMap(bucket["standard"])
		if util.ToInt(standard["balance"], -1) != 2 || util.ToInt(standard["lifetime_consumed"], -1) != 2 || util.ToInt(bucket["available"], -1) != 2 {
			t.Fatalf("billing state after partial success = %#v", state)
		}
	})

	t.Run("handler failure consumes zero", func(t *testing.T) {
		svc := newTestImageTaskService(t,
			func(context.Context, Identity, map[string]any) (map[string]any, error) {
				return map[string]any{"data": []map[string]any{{"url": "https://example.test/first.png"}}}, errors.New("upstream failed")
			},
			failingImageTaskHandler,
			failingImageTaskHandler,
			func() int { return 30 },
		)
		billing := newBilling(t, testBillingDefaults{standardBalance: 2})
		svc.SetBillingService(billing)
		if _, err := svc.SubmitGeneration(context.Background(), user, "failed", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 2, nil); err != nil {
			t.Fatalf("SubmitGeneration() error = %v", err)
		}
		waitForTaskStatus(t, svc, user, "failed", TaskStatusError)
		state := billing.Get("alice")
		standard := util.StringMap(bucketA(t, state)["standard"])
		if util.ToInt(standard["balance"], -1) != 2 || util.ToInt(standard["lifetime_consumed"], -1) != 0 {
			t.Fatalf("billing state after failure = %#v", state)
		}
	})

	t.Run("cancel consumes zero", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		svc := newTestImageTaskService(t,
			func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
			},
			failingImageTaskHandler,
			failingImageTaskHandler,
			func() int { return 30 },
		)
		billing := newBilling(t, testBillingDefaults{standardBalance: 2})
		svc.SetBillingService(billing)
		if _, err := svc.SubmitGeneration(context.Background(), user, "cancel", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 2, nil); err != nil {
			t.Fatalf("SubmitGeneration() error = %v", err)
		}
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for task start")
		}
		cancelled, err := svc.CancelTask(user, "cancel")
		if err != nil {
			t.Fatalf("CancelTask() error = %v", err)
		}
		close(release)
		if cancelled["status"] != TaskStatusCancelled {
			t.Fatalf("cancelled task = %#v", cancelled)
		}
		got := svc.ListTasks(user, []string{"cancel"})
		item := got["items"].([]map[string]any)[0]
		if util.ToInt(item["billing_consumed_amount"], -1) != 0 {
			t.Fatalf("settled cancelled task = %#v", item)
		}
		state := billing.Get("alice")
		standard := util.StringMap(bucketA(t, state)["standard"])
		if util.ToInt(standard["balance"], -1) != 2 || util.ToInt(standard["lifetime_consumed"], -1) != 0 {
			t.Fatalf("billing state after cancel = %#v", state)
		}
	})

	t.Run("image task returning text consumes zero", func(t *testing.T) {
		svc := newTestImageTaskService(t,
			func(context.Context, Identity, map[string]any) (map[string]any, error) {
				return map[string]any{"message": "text response", "output_type": "text"}, nil
			},
			failingImageTaskHandler,
			failingImageTaskHandler,
			func() int { return 30 },
		)
		billing := newBilling(t, testBillingDefaults{standardBalance: 1})
		svc.SetBillingService(billing)
		if _, err := svc.SubmitGeneration(context.Background(), user, "text", "who are you", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil); err != nil {
			t.Fatalf("SubmitGeneration() error = %v", err)
		}
		waitForTaskStatus(t, svc, user, "text", TaskStatusSuccess)
		state := billing.Get("alice")
		standard := util.StringMap(bucketA(t, state)["standard"])
		if util.ToInt(standard["balance"], -1) != 1 || util.ToInt(standard["lifetime_consumed"], -1) != 0 {
			t.Fatalf("billing state after text output = %#v", state)
		}
	})

	t.Run("subscription task consumes used quota", func(t *testing.T) {
		svc := newTestImageTaskService(t,
			func(context.Context, Identity, map[string]any) (map[string]any, error) {
				return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
			},
			failingImageTaskHandler,
			failingImageTaskHandler,
			func() int { return 30 },
		)
		billing := newBilling(t, testBillingDefaults{standardBalance: 0})
		if _, err := billing.ApplyAdjustment("alice", operator, map[string]any{"type": "switch_to_subscription", "bucket": util.ImageBucketA, "quota_limit": 2, "quota_period": BillingPeriodMonthly, "reason": "test"}); err != nil {
			t.Fatalf("switch_to_subscription error = %v", err)
		}
		svc.SetBillingService(billing)
		if _, err := svc.SubmitGeneration(context.Background(), user, "subscription", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 2, nil); err != nil {
			t.Fatalf("SubmitGeneration() error = %v", err)
		}
		waitForTaskStatus(t, svc, user, "subscription", TaskStatusSuccess)
		state := billing.Get("alice")
		bucket := bucketA(t, state)
		sub := util.StringMap(bucket["subscription"])
		if util.ToInt(sub["quota_used"], -1) != 1 || util.ToInt(bucket["available"], -1) != 1 {
			t.Fatalf("billing state after subscription task = %#v", state)
		}
	})

	t.Run("precharge protects running task from delivery-time drain", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		svc := newTestImageTaskService(t,
			func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return map[string]any{"data": []map[string]any{
					{"url": "https://example.test/first.png"},
					{"url": "https://example.test/second.png"},
				}}, nil
			},
			failingImageTaskHandler,
			failingImageTaskHandler,
			func() int { return 30 },
		)
		billing := newBilling(t, testBillingDefaults{standardBalance: 3})
		svc.SetBillingService(billing)
		if _, err := svc.SubmitGeneration(context.Background(), user, "delivery-drain-protected", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 2, nil); err != nil {
			t.Fatalf("SubmitGeneration() error = %v", err)
		}
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for task start")
		}
		if err := billing.Charge(user, 1, BillingReference{Bucket: util.ImageBucketA, ChargeKey: "external:drain:partial"}); err != nil {
			t.Fatalf("external Charge() error = %v", err)
		}
		close(release)
		waitForTaskStatus(t, svc, user, "delivery-drain-protected", TaskStatusSuccess)
		got := svc.ListTasks(user, []string{"delivery-drain-protected"})
		item := got["items"].([]map[string]any)[0]
		data := item["data"].([]map[string]any)
		if len(data) != 2 || data[0]["url"] != "https://example.test/first.png" || data[1]["url"] != "https://example.test/second.png" {
			t.Fatalf("task lost prepaid outputs = %#v", item)
		}
		if util.ToInt(item["billing_consumed_amount"], -1) != 2 {
			t.Fatalf("task billing = %#v", item)
		}
		statuses := util.AsStringSlice(item["output_statuses"])
		if len(statuses) != 2 || statuses[0] != TaskStatusSuccess || statuses[1] != TaskStatusSuccess {
			t.Fatalf("output_statuses = %#v, want both prepaid outputs successful", statuses)
		}
		state := billing.Get("alice")
		bucket := bucketA(t, state)
		standard := util.StringMap(bucket["standard"])
		if util.ToInt(standard["balance"], -1) != 0 || util.ToInt(standard["lifetime_consumed"], -1) != 3 || util.ToInt(bucket["available"], -1) != 0 {
			t.Fatalf("billing state after delivery-time drain = %#v", state)
		}
	})

	t.Run("insufficient balance rejects before queueing", func(t *testing.T) {
		handlerCalled := false
		svc := newTestImageTaskService(t,
			func(context.Context, Identity, map[string]any) (map[string]any, error) {
				handlerCalled = true
				return map[string]any{"data": []map[string]any{{"url": "https://example.test/unpaid.png"}}}, nil
			},
			failingImageTaskHandler,
			failingImageTaskHandler,
			func() int { return 30 },
		)
		billing := newBilling(t, testBillingDefaults{standardBalance: 0})
		svc.SetBillingService(billing)
		_, err := svc.SubmitGeneration(context.Background(), user, "delivery-drain-empty", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil)
		var limitErr BillingLimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("SubmitGeneration() error = %#v, want BillingLimitError", err)
		}
		if limitErr.Bucket != util.ImageBucketA {
			t.Fatalf("limit error bucket = %q, want %q", limitErr.Bucket, util.ImageBucketA)
		}
		if limitErr.Code != "user_balance_insufficient_bucket_a" {
			t.Fatalf("limit error code = %q, want user_balance_insufficient_bucket_a", limitErr.Code)
		}
		if handlerCalled {
			t.Fatal("handler was called for rejected image task")
		}
		got := svc.ListTasks(user, []string{"delivery-drain-empty"})
		if len(got["items"].([]map[string]any)) != 0 || len(got["missing_ids"].([]string)) != 1 {
			t.Fatalf("rejected image task should not be queued: %#v", got)
		}
		state := billing.Get("alice")
		standard := util.StringMap(bucketA(t, state)["standard"])
		if util.ToInt(standard["balance"], -1) != 0 || util.ToInt(standard["lifetime_consumed"], -1) != 0 {
			t.Fatalf("billing state after rejected task = %#v", state)
		}
	})
}

func TestImageTaskServiceBillingChatEquivalenceClasses(t *testing.T) {
	user := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}
	messages := []map[string]any{{"role": "user", "content": "hello"}}
	newBilling := func(t *testing.T, defaults testBillingDefaults) *BillingService {
		t.Helper()
		billing := newTestBillingService(t, defaults)
		billing.InitializeUserDefaults("alice")
		return billing
	}

	t.Run("pure text chat does not require billing", func(t *testing.T) {
		svc := newTestImageTaskService(t,
			failingImageTaskHandler,
			failingImageTaskHandler,
			func(context.Context, Identity, map[string]any) (map[string]any, error) {
				return map[string]any{"output_type": "text", "data": []map[string]any{{"text_response": "hello"}}}, nil
			},
			func() int { return 30 },
		)
		billing := newBilling(t, testBillingDefaults{})
		svc.SetBillingService(billing)
		if _, err := svc.SubmitChat(context.Background(), user, "text-chat", "hello", "auto", messages, false); err != nil {
			t.Fatalf("SubmitChat() error = %v", err)
		}
		waitForTaskStatus(t, svc, user, "text-chat", TaskStatusSuccess)
		state := billing.Get("alice")
		if util.ToInt(bucketA(t, state)["available"], -1) != 0 {
			t.Fatalf("text chat should not change default zero billing state = %#v", state)
		}
	})

	t.Run("billable chat consumes actual image outputs", func(t *testing.T) {
		svc := newTestImageTaskService(t,
			failingImageTaskHandler,
			failingImageTaskHandler,
			func(context.Context, Identity, map[string]any) (map[string]any, error) {
				return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
			},
			func() int { return 30 },
		)
		billing := newBilling(t, testBillingDefaults{standardBalance: 2})
		svc.SetBillingService(billing)
		if _, err := svc.SubmitChat(context.Background(), user, "image-chat", "draw", "auto", messages, true, 2); err != nil {
			t.Fatalf("SubmitChat() error = %v", err)
		}
		waitForTaskStatus(t, svc, user, "image-chat", TaskStatusSuccess)
		state := billing.Get("alice")
		standard := util.StringMap(bucketA(t, state)["standard"])
		if util.ToInt(standard["balance"], -1) != 1 || util.ToInt(standard["lifetime_consumed"], -1) != 1 {
			t.Fatalf("image chat billing = %#v", state)
		}
	})

	t.Run("billable chat insufficient balance rejects before queueing", func(t *testing.T) {
		handlerCalled := false
		svc := newTestImageTaskService(t,
			failingImageTaskHandler,
			failingImageTaskHandler,
			func(context.Context, Identity, map[string]any) (map[string]any, error) {
				handlerCalled = true
				return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
			},
			func() int { return 30 },
		)
		billing := newBilling(t, testBillingDefaults{standardBalance: 1})
		svc.SetBillingService(billing)
		_, err := svc.SubmitChat(context.Background(), user, "image-chat-rejected", "draw", "auto", messages, true, 2)
		var limitErr BillingLimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("SubmitChat() error = %#v, want BillingLimitError", err)
		}
		if limitErr.Bucket != util.ImageBucketA {
			t.Fatalf("limit error bucket = %q, want %q", limitErr.Bucket, util.ImageBucketA)
		}
		if limitErr.Code != "user_balance_insufficient_bucket_a" {
			t.Fatalf("limit error code = %q, want user_balance_insufficient_bucket_a", limitErr.Code)
		}
		if handlerCalled {
			t.Fatal("handler was called for rejected billable chat")
		}
		got := svc.ListTasks(user, []string{"image-chat-rejected"})
		if len(got["items"].([]map[string]any)) != 0 || len(got["missing_ids"].([]string)) != 1 {
			t.Fatalf("rejected billable chat should not be queued: %#v", got)
		}
	})
}

func TestImageTaskServiceMarksTimedOutTaskAsError(t *testing.T) {
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })
	svc.SetTaskTimeoutGetter(func() time.Duration { return 20 * time.Millisecond })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}

	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-1", "draw", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	waitForTaskStatus(t, svc, identity, "task-1", TaskStatusError)
	got := svc.ListTasks(identity, []string{"task-1"})
	item := got["items"].([]map[string]any)[0]
	if item["error"] != "图片生成超时，请稍后重试或降低分辨率" {
		t.Fatalf("timeout error = %#v", item)
	}
}

func TestImageTaskServicePreservesTextOutputType(t *testing.T) {
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		return map[string]any{"message": "text response", "output_type": "text"}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}

	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-1", "who are you", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	waitForTaskStatus(t, svc, identity, "task-1", TaskStatusSuccess)
	got := svc.ListTasks(identity, []string{"task-1"})
	item := got["items"].([]map[string]any)[0]
	if item["output_type"] != "text" {
		t.Fatalf("output_type = %#v, want text in %#v", item["output_type"], item)
	}
	data := item["data"].([]map[string]any)
	if len(data) != 1 || data[0]["text_response"] != "text response" {
		t.Fatalf("text response data = %#v", data)
	}
}

func TestImageTaskServiceStoresTextOutputFromHandlerError(t *testing.T) {
	handler := func(ctx context.Context, identity Identity, payload map[string]any) (map[string]any, error) {
		return map[string]any{"message": "text response", "output_type": "text"}, errors.New("text response")
	}
	svc := newTestImageTaskService(t, handler, handler, handler, func() int { return 30 })
	identity := Identity{ID: "alice", Name: "Alice", Role: "user"}

	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-1", "who are you", "gpt-image-2", "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	waitForTaskStatus(t, svc, identity, "task-1", TaskStatusSuccess)
	got := svc.ListTasks(identity, []string{"task-1"})
	item := got["items"].([]map[string]any)[0]
	if util.Clean(item["error"]) != "" {
		t.Fatalf("error = %#v, want empty in %#v", item["error"], item)
	}
	if item["output_type"] != "text" {
		t.Fatalf("output_type = %#v, want text in %#v", item["output_type"], item)
	}
	data := item["data"].([]map[string]any)
	if len(data) != 1 || data[0]["text_response"] != "text response" {
		t.Fatalf("text response data = %#v", data)
	}
	statuses := item["output_statuses"].([]string)
	if len(statuses) != 1 || statuses[0] != "success" {
		t.Fatalf("output_statuses = %#v, want success", statuses)
	}
}

func TestImageTaskServiceRestoresUnfinishedTasksAsErrors(t *testing.T) {
	backend := newTestStorageBackend(t)
	raw := map[string]any{"tasks": []map[string]any{
		{"id": "queued", "owner_id": "alice", "status": TaskStatusQueued, "mode": "generate", "created_at": "2026-01-01 00:00:00", "updated_at": "2026-01-01 00:00:00"},
		{"id": "running", "owner_id": "alice", "status": TaskStatusRunning, "mode": "edit", "created_at": "2026-01-01 00:00:00", "updated_at": "2026-01-01 00:00:00"},
	}}
	store, ok := backend.(storage.JSONDocumentBackend)
	if !ok {
		t.Fatalf("storage backend %T does not implement JSONDocumentBackend", backend)
	}
	if err := store.SaveJSONDocument("image_tasks.json", raw); err != nil {
		t.Fatalf("SaveJSONDocument() error = %v", err)
	}

	svc := NewStoredImageTaskService(backend, failingImageTaskHandler, failingImageTaskHandler, failingImageTaskHandler, func() int { return 30 })
	got := svc.ListTasks(Identity{ID: "alice"}, []string{"queued", "running"})
	items := got["items"].([]map[string]any)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	for _, item := range items {
		if item["status"] != TaskStatusError {
			t.Fatalf("unfinished task was not restored as error: %#v", item)
		}
		if item["error"] == nil {
			t.Fatalf("restored task missing error text: %#v", item)
		}
	}
}

// stubAutoImageRouteResolver 是一个可注入预置返回值的 AutoImageRouteResolver
// 测试桩。lastModel / lastN 记录最后一次 Resolve 调用的入参，便于断言
// submit 阶段是否把客户端原始 model 与 n 透传给路由层。
type stubAutoImageRouteResolver struct {
	resolved string
	bucket   string
	err      error

	lastModel string
	lastN     int
	calls     int
}

func (s *stubAutoImageRouteResolver) Resolve(_ Identity, originalModel string, n int) (string, string, error) {
	s.lastModel = originalModel
	s.lastN = n
	s.calls++
	return s.resolved, s.bucket, s.err
}

// TestImageTaskServiceSubmitInjectsBucketAndResolvedModel 校验 submit 阶段把
// AutoImageRouteResolver 解析出的 (resolvedExternalModel, bucket) 写入 payload
// 与 task 视图，覆盖 Requirement 6.3 / 6.4 / 6.7。
func TestImageTaskServiceSubmitInjectsBucketAndResolvedModel(t *testing.T) {
	handlerPayload := make(chan map[string]any, 1)
	handler := func(_ context.Context, _ Identity, payload map[string]any) (map[string]any, error) {
		handlerPayload <- payload
		return map[string]any{"data": []map[string]any{{"url": "https://example.test/image.png"}}}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, failingImageTaskHandler, func() int { return 30 })
	svc.SetAutoRouteResolver(&stubAutoImageRouteResolver{
		resolved: util.ImageModelCodexGPTImage2,
		bucket:   util.ImageBucketB,
	})

	identity := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}
	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-route", "draw", util.ImageModelAuto, "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}

	select {
	case payload := <-handlerPayload:
		if payload["bucket"] != util.ImageBucketB {
			t.Fatalf("payload bucket = %#v, want %s", payload["bucket"], util.ImageBucketB)
		}
		if payload["resolved_model"] != util.ImageModelCodexGPTImage2 {
			t.Fatalf("payload resolved_model = %#v, want %s", payload["resolved_model"], util.ImageModelCodexGPTImage2)
		}
		if payload["model"] != util.ImageModelAuto {
			t.Fatalf("payload model = %#v, want auto (original input must be preserved)", payload["model"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler payload")
	}

	waitForTaskStatus(t, svc, identity, "task-route", TaskStatusSuccess)
	got := svc.ListTasks(identity, []string{"task-route"})
	item := got["items"].([]map[string]any)[0]
	if item["bucket"] != util.ImageBucketB {
		t.Fatalf("task view bucket = %#v, want %s", item["bucket"], util.ImageBucketB)
	}
	if item["resolved_model"] != util.ImageModelCodexGPTImage2 {
		t.Fatalf("task view resolved_model = %#v, want %s", item["resolved_model"], util.ImageModelCodexGPTImage2)
	}
}

// TestImageTaskServiceSubmitRejectsUnknownModel 校验 submit 阶段对非
// External_Image_Model 集合的取值在调用解析器之前就 Fail-Fast，覆盖
// Requirement 6.1 / 6.2 关于 400 的约束。
func TestImageTaskServiceSubmitRejectsUnknownModel(t *testing.T) {
	resolver := &stubAutoImageRouteResolver{
		resolved: util.ImageModelGPTImage2,
		bucket:   util.ImageBucketA,
	}
	svc := newTestImageTaskService(t, failingImageTaskHandler, failingImageTaskHandler, failingImageTaskHandler, func() int { return 30 })
	svc.SetAutoRouteResolver(resolver)

	identity := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}
	_, err := svc.SubmitGeneration(context.Background(), identity, "task-bad-model", "draw", "not-real", "1024x1024", "high", "https://base.test", 1, nil)
	if err == nil {
		t.Fatal("SubmitGeneration() error = nil, want invalid model error")
	}
	if got, want := err.Error(), "model not-real is not a billable image model"; got != want {
		t.Fatalf("SubmitGeneration() error = %q, want %q", got, want)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver was unexpectedly called %d times for invalid model", resolver.calls)
	}
	got := svc.ListTasks(identity, nil)
	if items := got["items"].([]map[string]any); len(items) != 0 {
		t.Fatalf("invalid model should not queue a task: %#v", items)
	}
}

// TestImageTaskServiceSubmitFailsWhenResolverMissing 校验未装配 Auto 路由
// 解析器时 submit 阶段直接 Fail-Fast，覆盖 Requirement 6.3 的「auto 解析必须
// 在扣费前完成」约束。
func TestImageTaskServiceSubmitFailsWhenResolverMissing(t *testing.T) {
	// 直接构造服务而不经 newTestImageTaskService，跳过默认解析器装配。
	svc := NewStoredImageTaskService(newTestStorageBackend(t), failingImageTaskHandler, failingImageTaskHandler, failingImageTaskHandler, func() int { return 30 })

	identity := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}
	_, err := svc.SubmitGeneration(context.Background(), identity, "task-no-resolver", "draw", util.ImageModelAuto, "1024x1024", "high", "https://base.test", 1, nil)
	if err == nil {
		t.Fatal("SubmitGeneration() error = nil, want missing-resolver error")
	}
	if got, want := err.Error(), "auto route resolver not configured"; got != want {
		t.Fatalf("SubmitGeneration() error = %q, want %q", got, want)
	}
}

// TestImageTaskServiceSubmitPropagatesBillingLimit 校验 Auto 路由解析器
// 返回的 BillingLimitError 被 submit 原样回传，便于 HTTP 层转换为 OpenAI
// 风格的 insufficient_quota 响应。
func TestImageTaskServiceSubmitPropagatesBillingLimit(t *testing.T) {
	resolverErr := NewBillingLimitError(util.ImageBucketB, BillingTypeStandard)
	svc := newTestImageTaskService(t, failingImageTaskHandler, failingImageTaskHandler, failingImageTaskHandler, func() int { return 30 })
	svc.SetAutoRouteResolver(&stubAutoImageRouteResolver{err: resolverErr})

	identity := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}
	_, err := svc.SubmitGeneration(context.Background(), identity, "task-billing-limit", "draw", util.ImageModelAuto, "1024x1024", "high", "https://base.test", 1, nil)
	var got BillingLimitError
	if !errors.As(err, &got) {
		t.Fatalf("SubmitGeneration() error = %T %v, want BillingLimitError", err, err)
	}
	if got.Bucket != util.ImageBucketB {
		t.Fatalf("BillingLimitError.Bucket = %q, want %q", got.Bucket, util.ImageBucketB)
	}
}

// TestImageTaskServiceSettlesRefundOnSameBucket 验证 settle 阶段的退款落入
// 任务 submit 时绑定的桶（bucket_b），不会污染 bucket_a。
//
// 场景：handler 仅返回 1 张图（partial success），count=2 → 应在 bucket_b 退 1 张。
// _Requirements: 6.4
func TestImageTaskServiceSettlesRefundOnSameBucket(t *testing.T) {
	user := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}
	svc := newTestImageTaskService(t,
		func(context.Context, Identity, map[string]any) (map[string]any, error) {
			return map[string]any{"data": []map[string]any{
				{"url": "https://example.test/first.png"},
			}}, nil
		},
		failingImageTaskHandler,
		failingImageTaskHandler,
		func() int { return 30 },
	)
	svc.SetAutoRouteResolver(&stubAutoImageRouteResolver{
		resolved: util.ImageModelCodexGPTImage2,
		bucket:   util.ImageBucketB,
	})
	billing := newTestBillingService(t, dualBucketStandardDefaults(5, 4))
	billing.InitializeUserDefaults("alice")
	svc.SetBillingService(billing)

	if _, err := svc.SubmitGeneration(context.Background(), user, "bucket-b-success", "draw", util.ImageModelAuto, "1024x1024", "high", "https://base.test", 2, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	waitForTaskStatus(t, svc, user, "bucket-b-success", TaskStatusSuccess)

	state := billing.Get("alice")
	bb := bucketB(t, state)
	bbStandard := util.StringMap(bb["standard"])
	if util.ToInt(bbStandard["balance"], -1) != 3 {
		t.Fatalf("bucket_b balance = %v, want 3 (4 - 2 charged + 1 refunded)", bbStandard["balance"])
	}
	if util.ToInt(bbStandard["lifetime_consumed"], -1) != 1 {
		t.Fatalf("bucket_b lifetime_consumed = %v, want 1 (only the delivered image)", bbStandard["lifetime_consumed"])
	}

	ba := bucketA(t, state)
	baStandard := util.StringMap(ba["standard"])
	if util.ToInt(baStandard["balance"], -1) != 5 {
		t.Fatalf("bucket_a balance = %v, want 5 (untouched)", baStandard["balance"])
	}
	if util.ToInt(baStandard["lifetime_consumed"], -1) != 0 {
		t.Fatalf("bucket_a lifetime_consumed = %v, want 0 (untouched)", baStandard["lifetime_consumed"])
	}
}

// TestImageTaskServiceCancelRefundsBucketB 验证取消未完成任务时退款也走
// 同一桶（bucket_b），不会错退到 bucket_a。
//
// _Requirements: 6.4
func TestImageTaskServiceCancelRefundsBucketB(t *testing.T) {
	user := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}
	started := make(chan struct{})
	release := make(chan struct{})
	svc := newTestImageTaskService(t,
		func(ctx context.Context, _ Identity, _ map[string]any) (map[string]any, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return map[string]any{"data": []map[string]any{{"url": "https://example.test/late.png"}}}, nil
		},
		failingImageTaskHandler,
		failingImageTaskHandler,
		func() int { return 30 },
	)
	svc.SetAutoRouteResolver(&stubAutoImageRouteResolver{
		resolved: util.ImageModelCodexGPTImage2,
		bucket:   util.ImageBucketB,
	})
	billing := newTestBillingService(t, dualBucketStandardDefaults(5, 4))
	billing.InitializeUserDefaults("alice")
	svc.SetBillingService(billing)

	if _, err := svc.SubmitGeneration(context.Background(), user, "bucket-b-cancel", "draw", util.ImageModelAuto, "1024x1024", "high", "https://base.test", 2, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task start")
	}

	cancelled, err := svc.CancelTask(user, "bucket-b-cancel")
	if err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
	close(release)
	if cancelled["status"] != TaskStatusCancelled {
		t.Fatalf("cancelled task status = %#v", cancelled)
	}

	got := svc.ListTasks(user, []string{"bucket-b-cancel"})
	item := got["items"].([]map[string]any)[0]
	if util.ToInt(item["billing_consumed_amount"], -1) != 0 {
		t.Fatalf("settled cancelled task = %#v", item)
	}

	state := billing.Get("alice")
	bb := bucketB(t, state)
	bbStandard := util.StringMap(bb["standard"])
	if util.ToInt(bbStandard["balance"], -1) != 4 {
		t.Fatalf("bucket_b balance = %v, want 4 (fully refunded)", bbStandard["balance"])
	}
	if util.ToInt(bbStandard["lifetime_consumed"], -1) != 0 {
		t.Fatalf("bucket_b lifetime_consumed = %v, want 0 (cancel consumes nothing)", bbStandard["lifetime_consumed"])
	}

	ba := bucketA(t, state)
	baStandard := util.StringMap(ba["standard"])
	if util.ToInt(baStandard["balance"], -1) != 5 {
		t.Fatalf("bucket_a balance = %v, want 5 (untouched)", baStandard["balance"])
	}
}

// TestImageTaskServiceCapturesUpstreamKind 校验 runTask 成功路径下把
// Image_Engine 写入 result["upstream_kind"] 的物理上游标识同步到 task，
// 并通过 publicTask / ListTasks 暴露到管理端审计视图。
//
// 顺带断言 bucket / resolved_model 与 upstream_kind 一并写出，覆盖
// _Requirements: 6.5, 6.7, 9.4
func TestImageTaskServiceCapturesUpstreamKind(t *testing.T) {
	identity := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}
	handler := func(_ context.Context, _ Identity, _ map[string]any) (map[string]any, error) {
		// 模拟 Image_Engine.CollectImageOutputs 在所有图片成功交付后写出
		// 的聚合 result：data + upstream_kind。
		return map[string]any{
			"data":          []map[string]any{{"url": "https://example.test/image.png"}},
			"upstream_kind": util.UpstreamKindOpenAIAPI,
		}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, failingImageTaskHandler, func() int { return 30 })
	svc.SetAutoRouteResolver(&stubAutoImageRouteResolver{
		resolved: util.ImageModelGeminiFlashImage,
		bucket:   util.ImageBucketB,
	})

	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-upstream", "draw", util.ImageModelAuto, "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	waitForTaskStatus(t, svc, identity, "task-upstream", TaskStatusSuccess)

	got := svc.ListTasks(identity, []string{"task-upstream"})
	item := got["items"].([]map[string]any)[0]
	if item["upstream_kind"] != util.UpstreamKindOpenAIAPI {
		t.Fatalf("task view upstream_kind = %#v, want %s", item["upstream_kind"], util.UpstreamKindOpenAIAPI)
	}
	if item["bucket"] != util.ImageBucketB {
		t.Fatalf("task view bucket = %#v, want %s", item["bucket"], util.ImageBucketB)
	}
	if item["resolved_model"] != util.ImageModelGeminiFlashImage {
		t.Fatalf("task view resolved_model = %#v, want %s", item["resolved_model"], util.ImageModelGeminiFlashImage)
	}
}

// TestImageTaskServicePreservesEmptyUpstreamKindBeforeRun 校验任务在
// queue / running 阶段（handler 未返回前）upstream_kind 为空字符串：
// publicTask 不写出该字段，避免提前暴露假值。
//
// _Requirements: 6.5, 6.7
func TestImageTaskServicePreservesEmptyUpstreamKindBeforeRun(t *testing.T) {
	identity := Identity{ID: "alice", Name: "Alice", Role: AuthRoleUser}
	started := make(chan struct{})
	release := make(chan struct{})
	handler := func(ctx context.Context, _ Identity, _ map[string]any) (map[string]any, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return map[string]any{
			"data":          []map[string]any{{"url": "https://example.test/late.png"}},
			"upstream_kind": util.UpstreamKindChatGPT,
		}, nil
	}
	svc := newTestImageTaskService(t, handler, handler, failingImageTaskHandler, func() int { return 30 })
	svc.SetAutoRouteResolver(&stubAutoImageRouteResolver{
		resolved: util.ImageModelGPTImage2,
		bucket:   util.ImageBucketA,
	})
	defer close(release)

	if _, err := svc.SubmitGeneration(context.Background(), identity, "task-pending", "draw", util.ImageModelAuto, "1024x1024", "high", "https://base.test", 1, nil); err != nil {
		t.Fatalf("SubmitGeneration() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task to start")
	}

	// handler 仍在阻塞，task 处于 running 状态，upstream_kind 应缺席。
	got := svc.ListTasks(identity, []string{"task-pending"})
	items := got["items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("ListTasks() items = %#v, want 1", items)
	}
	if status := util.Clean(items[0]["status"]); status != TaskStatusRunning && status != TaskStatusQueued {
		t.Fatalf("task status = %q, want queued or running", status)
	}
	if _, ok := items[0]["upstream_kind"]; ok {
		t.Fatalf("upstream_kind should be absent before handler returns: %#v", items[0])
	}
}

func newTestImageTaskService(t *testing.T, generation ImageTaskHandler, edit ImageTaskHandler, chat ImageTaskHandler, retentionGetter func() int, limitGetters ...func() int) *ImageTaskService {
	t.Helper()
	svc := NewStoredImageTaskService(newTestStorageBackend(t), generation, edit, chat, retentionGetter, limitGetters...)
	svc.SetAutoRouteResolver(staticImageTaskRouteResolver{})
	return svc
}

// staticImageTaskRouteResolver 是 image_task 测试夹具默认装配的 Auto 路由
// 解析器：把 "" / "auto" 视为 gpt-image-2，其它对外模型经 util.BucketForModel
// 校验后透传，未识别值返回与生产 util 包一致的描述性错误。
//
// 该解析器不与 protocol 包耦合，便于既有 image_task_test.go 中以
// gpt-image-2 / auto 为入参的用例继续无缝工作。需要校验非法 model 或
// 测试 BillingLimitError 路径的用例需通过 SetAutoRouteResolver 注入自定义桩。
type staticImageTaskRouteResolver struct{}

func (staticImageTaskRouteResolver) Resolve(_ Identity, originalModel string, _ int) (string, string, error) {
	model := strings.TrimSpace(originalModel)
	if model == "" || model == util.ImageModelAuto {
		return util.ImageModelGPTImage2, util.ImageBucketA, nil
	}
	bucket, err := util.BucketForModel(model)
	if err != nil {
		return "", "", err
	}
	return model, bucket, nil
}

func waitForStartedTask(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case prompt := <-started:
		return prompt
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task handler to start")
	}
	return ""
}

func waitForStartedImageIndex(t *testing.T, started <-chan int) int {
	t.Helper()
	select {
	case index := <-started:
		return index
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for image output to start")
	}
	return 0
}

func failingImageTaskHandler(context.Context, Identity, map[string]any) (map[string]any, error) {
	return nil, errors.New("unexpected handler call")
}

// bucketA 返回 publicBillingState 输出中 bucket_a 子对象，便于读取 standard /
// subscription / available / limit_state / type 等字段。图像任务计费目前
// 只绑定到 bucket_a，断言一律走该桶的视图。
func bucketA(t *testing.T, state map[string]any) map[string]any {
	t.Helper()
	bucket, ok := state["bucket_a"].(map[string]any)
	if !ok {
		t.Fatalf("bucket_a missing or wrong type: %#v", state["bucket_a"])
	}
	return bucket
}

// bucketB 返回 publicBillingState 输出中 bucket_b 子对象，用于双桶隔离断言。
// codex-gpt-image-2 / gemini-3.1-flash-image 任务的预扣费 / 退款只能命中 bucket_b。
func bucketB(t *testing.T, state map[string]any) map[string]any {
	t.Helper()
	bucket, ok := state["bucket_b"].(map[string]any)
	if !ok {
		t.Fatalf("bucket_b missing or wrong type: %#v", state["bucket_b"])
	}
	return bucket
}

func waitForTaskStatus(t *testing.T, svc *ImageTaskService, identity Identity, taskID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := svc.ListTasks(identity, []string{taskID})
		items := got["items"].([]map[string]any)
		if len(items) == 1 && items[0]["status"] == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach status %s", taskID, want)
}

func waitForTaskData(t *testing.T, svc *ImageTaskService, identity Identity, taskID string, ok func([]map[string]any) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := svc.ListTasks(identity, []string{taskID})
		items := got["items"].([]map[string]any)
		if len(items) == 1 {
			if data, _ := items[0]["data"].([]map[string]any); ok(data) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not publish expected data", taskID)
}

func waitForTaskOutputStatusCounts(t *testing.T, svc *ImageTaskService, identity Identity, taskID string, want map[string]int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := svc.ListTasks(identity, []string{taskID})
		items := got["items"].([]map[string]any)
		if len(items) == 1 {
			counts := map[string]int{}
			for _, status := range util.AsStringSlice(items[0]["output_statuses"]) {
				counts[status]++
			}
			matches := true
			for status, count := range want {
				if counts[status] != count {
					matches = false
					break
				}
			}
			if matches {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s output status counts did not reach %#v", taskID, want)
}
