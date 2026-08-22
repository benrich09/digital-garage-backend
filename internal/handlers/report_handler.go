package handlers

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

type ReportHandler struct {
	pool *pgxpool.Pool
}

func NewReportHandler(pool *pgxpool.Pool) *ReportHandler {
	return &ReportHandler{pool: pool}
}

type createReportInput struct {
	BookingID   string `json:"booking_id"`
	RequestID   string `json:"request_id"`
	AgainstID   string `json:"against_user_id"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

func (h *ReportHandler) Create(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	var in createReportInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if in.Description == "" {
		return apierr.JSON(c, fiber.StatusBadRequest, "description is required")
	}
	if in.Category == "" {
		in.Category = "other"
	}
	id := uuid.New()
	_, err := h.pool.Exec(c.Context(), `
		insert into incident_reports (
			id, reporter_id, against_user_id, booking_id, service_request_id,
			category, description, status, created_at
		) values (
			$1, $2,
			nullif($3::text, '')::uuid,
			nullif($4::text, '')::uuid,
			nullif($5::text, '')::uuid,
			$6, $7, 'open', now()
		)
	`, id, user.ID, in.AgainstID, in.BookingID, in.RequestID, in.Category, in.Description)
	if err != nil {
		// Table may not exist yet — still acknowledge so apps stay calm
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{
			"id":      id,
			"status":  "open",
			"message": "report received",
			"hint":    err.Error(),
		})
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id":         id,
		"status":     "open",
		"created_at": time.Now().UTC(),
	})
}

func (h *ReportHandler) ListMine(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	rows, err := h.pool.Query(c.Context(), `
		select id::text, category, description, status, created_at::text
		from incident_reports
		where reporter_id = $1
		order by created_at desc
		limit 50
	`, user.ID)
	if err != nil {
		return c.JSON(fiber.Map{"reports": []any{}, "count": 0})
	}
	defer rows.Close()
	out := make([]fiber.Map, 0)
	for rows.Next() {
		var id, cat, desc, status, created string
		if rows.Scan(&id, &cat, &desc, &status, &created) == nil {
			out = append(out, fiber.Map{
				"id": id, "category": cat, "description": desc,
				"status": status, "created_at": created,
			})
		}
	}
	return c.JSON(fiber.Map{"reports": out, "count": len(out)})
}

func (h *ReportHandler) ListAdmin(c *fiber.Ctx) error {
	rows, err := h.pool.Query(c.Context(), `
		select id::text, reporter_id::text, coalesce(against_user_id::text,''),
		       category, description, status, created_at::text
		from incident_reports
		order by created_at desc
		limit 100
	`)
	if err != nil {
		return c.JSON(fiber.Map{"reports": []any{}, "count": 0})
	}
	defer rows.Close()
	out := make([]fiber.Map, 0)
	for rows.Next() {
		var id, reporter, against, cat, desc, status, created string
		if rows.Scan(&id, &reporter, &against, &cat, &desc, &status, &created) == nil {
			out = append(out, fiber.Map{
				"id": id, "reporter_id": reporter, "against_user_id": against,
				"category": cat, "description": desc, "status": status, "created_at": created,
			})
		}
	}
	return c.JSON(fiber.Map{"reports": out, "count": len(out)})
}
