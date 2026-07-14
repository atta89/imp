package router

import (
	"github.com/gofiber/fiber/v2"

	"imp/internal/handler"
	"imp/internal/jwtauth"
	"imp/internal/middleware"
	"imp/internal/models"
)

type Deps struct {
	Health         *handler.HealthHandler
	Auth           *handler.AuthHandler
	Users          *handler.UserHandler
	Me             *handler.MeHandler
	Venues         *handler.VenueHandler
	Categories     *handler.CategoryHandler
	Departments    *handler.DepartmentHandler
	Assets         *handler.AssetHandler
	PurchaseOrders *handler.PurchaseOrderHandler
	Imports        *handler.ImportHandler
	Repairs        *handler.RepairHandler
	Dashboard      *handler.DashboardHandler
	Reports        *handler.ReportHandler
	Notifications  *handler.NotificationHandler
	Attachments    *handler.AttachmentHandler
	Scan           *handler.ScanHandler
	Issuer         *jwtauth.Issuer
}

func Register(app *fiber.App, d Deps) {
	// Unauthenticated infra routes.
	app.Get("/healthz", d.Health.Live)
	app.Get("/readyz", d.Health.Ready)

	v1 := app.Group("/api/v1")

	// Public auth endpoints.
	v1.Post("/auth/login", d.Auth.Login)
	v1.Post("/auth/refresh", d.Auth.Refresh)

	// Everything below requires a valid access token.
	authed := v1.Group("", middleware.RequireAuth(d.Issuer))
	authed.Get("/auth/me", d.Auth.Me)

	// Authenticated QR-scan lookup. Resolves a QR token to a read-only asset
	// view; the service authorizes the caller per-asset (admin, venue scope,
	// or current custodian) and resolves lost/retired assets for those callers.
	// The printed-label URL format is unchanged — only the access rules are.
	authed.Get("/scan/:qrToken", d.Scan.Scan)

	adminOnly := middleware.RequireRole(models.Admin)

	// Users — entirely admin-only (account management is a privileged op).
	// Self-service knobs (own profile, notification preferences) live under
	// /me/* and use the JWT identity, not /users/:id.
	authed.Get("/users", adminOnly, d.Users.List)
	authed.Get("/users/:id", adminOnly, d.Users.Get)
	authed.Post("/users", adminOnly, d.Users.Create)
	authed.Put("/users/:id", adminOnly, d.Users.Update)
	authed.Delete("/users/:id", adminOnly, d.Users.Delete)

	// Venues — read open to any authed user (scope-filtered in handler);
	// writes admin-only.
	authed.Get("/venues", d.Venues.List)
	authed.Get("/venues/:id", d.Venues.Get)
	authed.Post("/venues", adminOnly, d.Venues.Create)
	authed.Put("/venues/:id", adminOnly, d.Venues.Update)
	authed.Delete("/venues/:id", adminOnly, d.Venues.Delete)

	// Categories — same pattern.
	authed.Get("/categories", d.Categories.List)
	authed.Get("/categories/:id", d.Categories.Get)
	authed.Post("/categories", adminOnly, d.Categories.Create)
	authed.Put("/categories/:id", adminOnly, d.Categories.Update)
	authed.Delete("/categories/:id", adminOnly, d.Categories.Delete)

	// Departments — nested under venues; read gated by venue scope, write admin-only.
	authed.Get("/venues/:venueId/departments", d.Departments.List)
	authed.Get("/venues/:venueId/departments/:id", d.Departments.Get)
	authed.Post("/venues/:venueId/departments", adminOnly, d.Departments.Create)
	authed.Put("/venues/:venueId/departments/:id", adminOnly, d.Departments.Update)
	authed.Delete("/venues/:venueId/departments/:id", adminOnly, d.Departments.Delete)

	// Assets — read scope-filtered in handler; create/update gated by venue
	// scope inline; delete admin-only. Register static bulk paths before the
	// dynamic ":id" routes so Fiber's matcher hits them first.
	// Bulk asset operations. Static paths registered before the dynamic ":id"
	// routes so Fiber's matcher hits them first. RBAC is scope-based inside
	// the service so non-admin venue managers can act on assets in their scope.
	authed.Post("/assets/bulk/transfer", d.Assets.BulkTransfer)
	authed.Post("/assets/bulk/status", d.Assets.BulkStatus)
	authed.Post("/assets/bulk/assign", d.Assets.BulkAssign)
	authed.Post("/assets/condition/bulk", d.Assets.BulkCondition)
	authed.Post("/assets/qr/bulk", d.Assets.QRBulk)
	authed.Post("/assets/bulk/ids", d.Assets.BulkIds)

	// Async bulk-job read endpoints. RBAC (requester-or-admin) is enforced in
	// the handlers. Registered before "/assets/:id" so the static prefix wins.
	authed.Get("/assets/bulk/jobs", d.Assets.BulkJobList)
	authed.Get("/assets/bulk/jobs/:jobId", d.Assets.BulkJobGet)
	authed.Get("/assets/bulk/jobs/:jobId/result", d.Assets.BulkJobResult)

	authed.Get("/assets", d.Assets.List)
	authed.Get("/assets/:id", d.Assets.Get)
	authed.Get("/assets/:id/history", d.Assets.History)
	authed.Get("/assets/:id/qr", d.Assets.QR)
	authed.Post("/assets", d.Assets.Create)
	authed.Put("/assets/:id", d.Assets.Update)
	authed.Delete("/assets/:id", adminOnly, d.Assets.Delete)

	// State-changing asset endpoints — each writes a movements record.
	authed.Post("/assets/:id/status", d.Assets.ChangeStatus)
	authed.Post("/assets/:id/transfer", d.Assets.Transfer)
	authed.Post("/assets/:id/assign", d.Assets.Assign)
	authed.Post("/assets/:id/condition", d.Assets.UpdateCondition)

	// Purchase orders — admin creates/edits/receives; everyone authed can read.
	authed.Get("/purchase-orders", d.PurchaseOrders.List)
	authed.Get("/purchase-orders/:id", d.PurchaseOrders.Get)
	authed.Post("/purchase-orders", adminOnly, d.PurchaseOrders.Create)
	authed.Put("/purchase-orders/:id", adminOnly, d.PurchaseOrders.Update)
	authed.Post("/purchase-orders/:id/receive", adminOnly, d.PurchaseOrders.Receive)

	// Bulk PO import (admin-only). Register the static /imports/purchase-orders/*
	// paths before the dynamic /imports/:id so Fiber's matcher hits the
	// specific ones first.
	authed.Get("/imports/purchase-orders/template", adminOnly, d.Imports.Template)
	authed.Post("/imports/purchase-orders/validate", adminOnly, d.Imports.Validate)
	authed.Post("/imports/purchase-orders/commit", adminOnly, d.Imports.Commit)
	authed.Get("/imports/:id", adminOnly, d.Imports.Get)
	authed.Get("/imports/:id/report", adminOnly, d.Imports.Report)

	// Repairs — authed read; create/update gated by venue scope on the asset.
	authed.Get("/repairs", d.Repairs.List)
	authed.Get("/repairs/:id", d.Repairs.Get)
	authed.Post("/repairs", d.Repairs.Create)
	authed.Put("/repairs/:id", d.Repairs.Update)

	// Dashboard + reports — authed, mostly global view. /reports/by-department
	// is the exception: non-admin callers must pass a `venue` query param,
	// which the handler gates via CanAccessVenue.
	authed.Get("/dashboard/summary", d.Dashboard.Summary)
	authed.Get("/reports/inventory-by-venue", d.Reports.InventoryByVenue)
	authed.Get("/reports/assets-away", d.Reports.AssetsAway)
	authed.Get("/reports/assets-overdue", d.Reports.AssetsOverdue)
	authed.Get("/reports/in-repair", d.Reports.InRepair)
	authed.Get("/reports/by-responsible", d.Reports.ByResponsible)
	authed.Get("/reports/by-department", d.Reports.ByDepartment)

	// Notifications outbox log (admin only).
	authed.Get("/notifications", adminOnly, d.Notifications.List)

	// Attachments — upload + RBAC-gated download.
	authed.Post("/attachments", d.Attachments.Upload)
	authed.Get("/attachments/:id/download", d.Attachments.Download)

	// Self-service /me/*. Identity is taken from the JWT, so these routes
	// never need an :id path parameter.
	authed.Post("/me/password", d.Me.ChangePassword)
	authed.Get("/me/notification-preferences", d.Me.GetNotificationPreferences)
	authed.Put("/me/notification-preferences", d.Me.UpdateNotificationPreferences)
}
