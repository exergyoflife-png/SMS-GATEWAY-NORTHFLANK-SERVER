package messages //nolint:testpackage // The private handler is exercised without exporting a test-only API.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/android-sms-gateway/server/internal/sms-gateway/handlers/base"
	"github.com/android-sms-gateway/server/internal/sms-gateway/modules/devices"
	modulemessages "github.com/android-sms-gateway/server/internal/sms-gateway/modules/messages"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

const mobilePatchRequest = `[
	{"id":"message-1","state":"Sent","recipients":[{"phoneNumber":"+15555550101","state":"Sent"}],"states":{}},
	{"id":"message-2","state":"Delivered","recipients":[{"phoneNumber":"+15555550102","state":"Delivered"}],"states":{}},
	{"id":"message-3","state":"Failed","recipients":[{"phoneNumber":"+15555550103","state":"Failed"}],"states":{}}
]`

type mobileMessagesServiceStub struct {
	updateState func(device *devices.Device, message modulemessages.MessageStateInput) error
}

func (s *mobileMessagesServiceStub) SelectPending(
	string,
	modulemessages.Order,
) ([]modulemessages.Message, error) {
	return nil, nil
}

func (s *mobileMessagesServiceStub) UpdateState(
	device *devices.Device,
	message modulemessages.MessageStateInput,
) error {
	return s.updateState(device, message)
}

func TestMobileControllerPatch(t *testing.T) {
	t.Parallel()

	t.Run("returns no content when all updates succeed", func(t *testing.T) {
		t.Parallel()

		var updated []string
		svc := &mobileMessagesServiceStub{
			updateState: func(_ *devices.Device, message modulemessages.MessageStateInput) error {
				updated = append(updated, message.ID)
				return nil
			},
		}

		status, _ := performMobilePatch(t, svc)

		if status != fiber.StatusNoContent {
			t.Fatalf("expected status %d, got %d", fiber.StatusNoContent, status)
		}
		assertUpdatedMessageIDs(t, updated, "message-1", "message-2", "message-3")
	})

	t.Run("ignores message not found and continues", func(t *testing.T) {
		t.Parallel()

		var updated []string
		svc := &mobileMessagesServiceStub{
			updateState: func(_ *devices.Device, message modulemessages.MessageStateInput) error {
				updated = append(updated, message.ID)
				if message.ID == "message-1" {
					return fmt.Errorf("select message: %w", modulemessages.ErrMessageNotFound)
				}
				return nil
			},
		}

		status, _ := performMobilePatch(t, svc)

		if status != fiber.StatusNoContent {
			t.Fatalf("expected status %d, got %d", fiber.StatusNoContent, status)
		}
		assertUpdatedMessageIDs(t, updated, "message-1", "message-2", "message-3")
	})

	t.Run("returns internal server error and stops on persistence failure", func(t *testing.T) {
		t.Parallel()

		var updated []string
		svc := &mobileMessagesServiceStub{
			updateState: func(_ *devices.Device, message modulemessages.MessageStateInput) error {
				updated = append(updated, message.ID)
				if message.ID == "message-2" {
					return errors.New("database unavailable")
				}
				return nil
			},
		}

		status, body := performMobilePatch(t, svc)

		if status != fiber.StatusInternalServerError {
			t.Fatalf("expected status %d, got %d", fiber.StatusInternalServerError, status)
		}
		if strings.Contains(body, "database unavailable") {
			t.Fatalf("response exposed the service error: %q", body)
		}
		if !strings.Contains(body, "failed to update message status") {
			t.Fatalf("expected generic error response, got %q", body)
		}
		assertUpdatedMessageIDs(t, updated, "message-1", "message-2")
	})
}

func performMobilePatch(t *testing.T, svc mobileMessagesService) (int, string) {
	t.Helper()

	controller := &MobileController{
		Handler:     base.Handler{Logger: zap.NewNop()},
		messagesSvc: svc,
	}
	app := fiber.New()
	device := devices.Device{
		DeviceInput: devices.DeviceInput{ID: "device-1"},
	}
	app.Patch("/", func(c *fiber.Ctx) error {
		return controller.patch(device, c)
	})

	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(mobilePatchRequest))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	return resp.StatusCode, string(body)
}

func assertUpdatedMessageIDs(t *testing.T, got []string, want ...string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("expected updated message IDs %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected updated message IDs %v, got %v", want, got)
		}
	}
}
