package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberrecover "github.com/gofiber/fiber/v2/middleware/recover"

	"imp/internal/bulkjob"
	"imp/internal/config"
	"imp/internal/database"
	"imp/internal/handler"
	"imp/internal/jwtauth"
	"imp/internal/middleware"
	"imp/internal/notification"
	"imp/internal/qr"
	"imp/internal/repository"
	"imp/internal/router"
	"imp/internal/scheduler"
	"imp/internal/service"
	"imp/internal/storage"
	"imp/internal/validate"
)

func main() {
	if err := run(); err != nil {
		slog.Error("startup_failed", slog.Any("err", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	bootCtx, cancelBoot := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelBoot()

	mongoConn, err := database.Connect(bootCtx, cfg.MongoURI, cfg.MongoDB)
	if err != nil {
		return err
	}
	logger.Info("mongo_connected", slog.String("db", cfg.MongoDB))

	if err := database.EnsureIndexes(bootCtx, mongoConn.DB); err != nil {
		_ = mongoConn.Close(context.Background())
		return err
	}
	logger.Info("indexes_ensured")

	// Dependency wiring.
	validator := validate.New()
	issuer := jwtauth.NewIssuer(cfg.JWTSecret, cfg.JWTAccess, cfg.JWTRefresh)

	userRepo := repository.NewUserRepository(mongoConn.DB)
	venueRepo := repository.NewVenueRepository(mongoConn.DB)
	categoryRepo := repository.NewCategoryRepository(mongoConn.DB)
	departmentRepo := repository.NewDepartmentRepository(mongoConn.DB)
	assetRepo := repository.NewAssetRepository(mongoConn.DB)
	movementRepo := repository.NewMovementRepository(mongoConn.DB)
	counterRepo := repository.NewCounterRepository(mongoConn.DB)
	poRepo := repository.NewPurchaseOrderRepository(mongoConn.DB)
	repairRepo := repository.NewRepairRepository(mongoConn.DB)
	outboxRepo := notification.NewRepository(mongoConn.DB)
	importJobRepo := repository.NewImportJobRepository(mongoConn.DB)
	attRepo := repository.NewAttachmentRepository(mongoConn.DB)
	bulkJobRepo := repository.NewBulkJobRepository(mongoConn.DB)

	fs, err := storage.NewLocalDisk(cfg.StorageBaseDir)
	if err != nil {
		_ = mongoConn.Close(context.Background())
		return fmt.Errorf("init storage: %w", err)
	}

	// Scan URLs (in QR codes and email links) resolve to the FRONTEND,
	// not this API. cfg.FrontendBaseURL falls back to AppBaseURL if unset.
	triggers := notification.NewTriggers(outboxRepo, userRepo, venueRepo, logger, cfg.FrontendBaseURL)
	mailer := buildMailer(cfg, logger)

	userSvc := service.NewUserService(userRepo, assetRepo, poRepo)
	authSvc := service.NewAuthService(userRepo, issuer)
	venueSvc := service.NewVenueService(venueRepo, assetRepo, departmentRepo)
	categorySvc := service.NewCategoryService(categoryRepo, assetRepo)
	departmentSvc := service.NewDepartmentService(departmentRepo, assetRepo, venueRepo)
	qrLogo := qr.LoadLogo(cfg.QRLogoPath, logger)
	attSvc := service.NewAttachmentService(attRepo, assetRepo, fs, service.AttachmentConfig{
		MaxBytes:      cfg.AttachmentMaxBytes,
		MaxPerRequest: cfg.AttachmentMaxPerRequest,
	})
	assetSvc := service.NewAssetService(assetRepo, movementRepo, venueRepo, categoryRepo, userRepo, departmentRepo, counterRepo, triggers, mongoConn.Client, cfg.FrontendBaseURL, qrLogo, attSvc)
	bulkJobSvc := service.NewBulkJobService(assetSvc, bulkJobRepo, fs, service.BulkJobConfig{
		MaxAssets:   cfg.BulkMaxAssets,
		BatchSize:   cfg.BulkBatchSize,
		MaxAttempts: cfg.BulkJobMaxAttempts,
		ErrorCap:    cfg.BulkJobErrorCap,
		Lease:       cfg.BulkJobLease,
		ResultTTL:   cfg.BulkResultTTL,
	}, logger)
	poSvc := service.NewPurchaseOrderService(poRepo, assetRepo, movementRepo, counterRepo, venueRepo, categoryRepo, userRepo, departmentRepo, mongoConn.Client)
	repairSvc := service.NewRepairService(repairRepo, assetRepo, movementRepo, triggers)
	dashboardSvc := service.NewDashboardService(assetRepo, venueRepo)
	reportSvc := service.NewReportService(assetRepo, venueRepo, userRepo, repairRepo, departmentRepo)
	overdueSvc := service.NewOverdueService(assetRepo, userRepo, venueRepo, outboxRepo, logger, cfg.FrontendBaseURL)
	importSvc := service.NewImportService(importJobRepo, poSvc, poRepo, assetRepo, userRepo, venueRepo, categoryRepo, departmentRepo)

	if err := seedAdmin(bootCtx, logger, cfg, userSvc); err != nil {
		_ = mongoConn.Close(context.Background())
		return err
	}

	healthH := handler.NewHealthHandler(mongoConn.Client)
	authH := handler.NewAuthHandler(authSvc, userRepo, validator)
	userH := handler.NewUserHandler(userSvc)
	meH := handler.NewMeHandler(userSvc)
	venueH := handler.NewVenueHandler(venueSvc)
	categoryH := handler.NewCategoryHandler(categorySvc)
	departmentH := handler.NewDepartmentHandler(departmentSvc)
	assetH := handler.NewAssetHandler(assetSvc, bulkJobSvc)
	scanH := handler.NewScanHandler(assetSvc)
	poH := handler.NewPurchaseOrderHandler(poSvc)
	importH := handler.NewImportHandler(importSvc, assetRepo, poRepo, venueRepo, categoryRepo, userRepo)
	repairH := handler.NewRepairHandler(repairSvc, assetSvc)
	dashboardH := handler.NewDashboardHandler(dashboardSvc)
	reportH := handler.NewReportHandler(reportSvc)
	notificationH := handler.NewNotificationHandler(outboxRepo)
	attH := handler.NewAttachmentHandler(attSvc)

	app := fiber.New(fiber.Config{
		AppName:               "imp",
		DisableStartupMessage: true,
		ErrorHandler:          middleware.ErrorHandler(logger),
		ReadTimeout:           15 * time.Second,
		WriteTimeout:          30 * time.Second,
		// Fiber's own default (4 MiB) applies to the whole request body,
		// including multipart framing, and runs BEFORE our handler ever
		// sees the request. Without raising it here, any upload between
		// Fiber's 4 MiB default and our configured ATTACHMENT_MAX_BYTES
		// is rejected with a generic Fiber error instead of the intended
		// apperror.BadRequest("file exceeds max size...") response.
		BodyLimit: int(cfg.AttachmentMaxBytes) + (1 << 20), // +1 MiB headroom for multipart framing
	})

	app.Use(fiberrecover.New())
	app.Use(middleware.RequestLogger(logger))

	router.Register(app, router.Deps{
		Health:         healthH,
		Auth:           authH,
		Users:          userH,
		Me:             meH,
		Venues:         venueH,
		Categories:     categoryH,
		Departments:    departmentH,
		Assets:         assetH,
		PurchaseOrders: poH,
		Imports:        importH,
		Repairs:        repairH,
		Dashboard:      dashboardH,
		Reports:        reportH,
		Notifications:  notificationH,
		Attachments:    attH,
		Scan:           scanH,
		Issuer:         issuer,
	})

	// Notification outbox worker. workerCtx is cancelled on shutdown so the
	// worker drains its current tick and exits cleanly.
	workerCtx, stopWorker := context.WithCancel(context.Background())
	worker := notification.NewWorker(outboxRepo, mailer, logger, notification.WorkerConfig{})
	go worker.Run(workerCtx)

	// Async bulk-asset job worker — claims queued jobs and runs them in batched
	// transactions. Same cancellable-context lifecycle as the outbox worker.
	bulkWorker := bulkjob.NewWorker(bulkJobSvc, logger, bulkjob.Config{
		Concurrency:  cfg.BulkWorkerConcurrency,
		PollInterval: cfg.BulkWorkerPollInterval,
	})
	go bulkWorker.Run(workerCtx)

	// Scheduler — daily overdue scan + digest. Start AFTER the worker so any
	// digest enqueued by the first tick has somewhere to drain to.
	sched := scheduler.New(overdueSvc, attSvc, bulkJobSvc, logger, cfg.OverdueCron, cfg.AttachmentSweepCron, cfg.BulkResultCleanupCron)
	if err := sched.Start(); err != nil {
		stopWorker()
		_ = mongoConn.Close(context.Background())
		return err
	}

	// Graceful shutdown.
	serverErr := make(chan error, 1)
	go func() {
		addr := ":" + cfg.Port
		logger.Info("http_server_listening", slog.String("addr", addr))
		if err := app.Listen(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		sched.Stop(context.Background())
		stopWorker()
		_ = mongoConn.Close(context.Background())
		return err
	case s := <-sig:
		logger.Info("shutdown_signal_received", slog.String("signal", s.String()))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sched.Stop(shutdownCtx)
	stopWorker()

	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		logger.Error("http_shutdown_error", slog.Any("err", err))
	} else {
		logger.Info("http_shutdown_complete")
	}

	if err := mongoConn.Close(shutdownCtx); err != nil {
		logger.Error("mongo_close_error", slog.Any("err", err))
	} else {
		logger.Info("mongo_closed")
	}

	logger.Info("shutdown_complete")
	return nil
}

// buildMailer returns a real Gmail mailer when both creds are set, else a
// log-only mailer so local dev doesn't need SMTP configured.
func buildMailer(cfg *config.Config, logger *slog.Logger) notification.Mailer {
	if cfg.GmailUser == "" || cfg.GmailPass == "" {
		logger.Warn("gmail_creds_missing_using_log_mailer")
		return notification.NewLogMailer(logger)
	}
	port := 587
	if v, err := strconv.Atoi(cfg.SMTPPort); err == nil {
		port = v
	}
	return notification.NewGmailMailer(cfg.GmailUser, cfg.GmailPass, cfg.SMTPHost, port)
}

func seedAdmin(ctx context.Context, logger *slog.Logger, cfg *config.Config, userSvc *service.UserService) error {
	if cfg.SeedAdminEmail == "" || cfg.SeedAdminPassword == "" {
		return nil
	}
	u, created, err := userSvc.SeedAdmin(ctx, cfg.SeedAdminName, cfg.SeedAdminEmail, cfg.SeedAdminPassword)
	if err != nil {
		return err
	}
	if created {
		logger.Info("admin_seeded", slog.String("email", string(u.Email)))
	} else {
		logger.Info("admin_seed_skipped", slog.String("reason", "admin already exists"))
	}
	return nil
}
