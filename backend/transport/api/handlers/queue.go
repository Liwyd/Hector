package handlers

import (
	"github.com/gofiber/fiber/v2"

	"hector/backend/services"
	"hector/backend/types"
)

// OrderQueueList — GET /api/order-queue
// The panel polls this; it is the only state the panel keeps itself.
func OrderQueueList(c *fiber.Ctx) error {
	return c.JSON(services.QueueList())
}

// OrderQueueAdd — POST /api/order-queue
func OrderQueueAdd(c *fiber.Ctx) error {
	var req types.CreateRequest
	if err := parseBody(c, &req); err != nil {
		return fail(c, err)
	}
	entry, err := services.QueueAdd(req)
	if err != nil {
		return fail(c, err)
	}
	return c.Status(fiber.StatusCreated).JSON(entry)
}

// OrderQueueRemove — DELETE /api/order-queue/:id
func OrderQueueRemove(c *fiber.Ctx) error {
	if err := services.QueueRemove(c.Params("id")); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// OrderQueueForgetSecret — POST /api/order-queue/:id/forget
// The root password is only handed out once by Hetzner; this drops it from
// the queue file once it has been written down.
func OrderQueueForgetSecret(c *fiber.Ctx) error {
	if err := services.QueueForgetSecret(c.Params("id")); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// OrderQueueCheck — POST /api/order-queue/check
// "Is it in stock yet?" without waiting for the next tick.
func OrderQueueCheck(c *fiber.Ctx) error {
	if err := services.QueueCheckNow(c.Context()); err != nil {
		return fail(c, err)
	}
	return c.JSON(services.QueueList())
}
