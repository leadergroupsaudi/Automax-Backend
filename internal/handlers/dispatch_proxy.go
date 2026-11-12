package handlers

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
)

// DispatchProxy forwards Dispatch Management calls to Automax Registry.
// Registry keeps the unit, unit type, and fire-station rows. This handler
// does not forward the caller's Authorization header.
type DispatchProxy struct {
	base   string
	client *http.Client
}

func NewDispatchProxy(baseURL string) *DispatchProxy {
	return &DispatchProxy{
		base:   strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (h *DispatchProxy) Forward(c *fiber.Ctx) error {
	if h == nil || h.base == "" {
		return utils.ErrorResponse(c, fiber.StatusServiceUnavailable, "dispatch service is not configured")
	}
	tail := strings.Trim(c.Params("*"), "/")
	target := h.base + "/dispatch"
	if tail != "" {
		target += "/" + tail
	}
	if query := c.Request().URI().QueryString(); len(query) > 0 {
		target += "?" + string(query)
	}
	req, err := http.NewRequestWithContext(c.UserContext(), c.Method(), target, bytes.NewReader(c.Body()))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadGateway, "dispatch service is unavailable")
	}
	if contentType := c.Get(fiber.HeaderContentType); contentType != "" {
		req.Header.Set(fiber.HeaderContentType, contentType)
	}
	req.Header.Set(fiber.HeaderAccept, fiber.MIMEApplicationJSON)
	resp, err := h.client.Do(req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadGateway, "dispatch service is unavailable")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadGateway, "dispatch service is unavailable")
	}
	if contentType := resp.Header.Get(fiber.HeaderContentType); contentType != "" {
		c.Set(fiber.HeaderContentType, contentType)
	} else {
		c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	return c.Status(resp.StatusCode).Send(body)
}
