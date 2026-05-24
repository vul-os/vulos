package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"

	"vulos-office/backend/config"
	"vulos-office/backend/handlers"
	"vulos-office/backend/middleware"
	"vulos-office/backend/obs"
	"vulos-office/backend/storage"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

//go:embed all:dist
var distFS embed.FS

func main() {
	obs.Init()

	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Printf("Config error: %v — using defaults", err)
		cfg = config.Default()
	}

	store, err := storage.New(cfg)
	if err != nil {
		log.Fatal("Storage init failed:", err)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	r.Use(cors.New(cors.Config{
		AllowAllOrigins:  true,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
	}))

	// Prometheus metrics (no auth required).
	r.GET("/metrics", gin.WrapH(obs.Handler()))

	// Auth routes (unauthenticated)
	authHandler := handlers.NewAuthHandler(cfg)
	api := r.Group("/api")
	api.POST("/auth/login", authHandler.Login)
	api.POST("/auth/logout", authHandler.Logout)
	api.GET("/auth/status", authHandler.Status)

	// Protected API routes
	protected := api.Group("/")
	if cfg.Auth.Enabled {
		protected.Use(middleware.Auth(cfg))
	}

	fileHandler := handlers.NewFileHandler(store)
	protected.GET("/files", fileHandler.List)
	protected.GET("/files/:id", fileHandler.Get)
	protected.POST("/files", fileHandler.Create)
	protected.PUT("/files/:id", fileHandler.Update)
	protected.DELETE("/files/:id", fileHandler.Delete)

	// OFFICE-08: version history endpoints.
	versionHandler := handlers.NewVersionHandler(store)
	protected.GET("/files/:id/versions", versionHandler.ListVersions)
	protected.POST("/files/:id/versions/:vid/restore", versionHandler.RestoreVersion)

	// OFFICE-28: activity feed + named snapshots.
	activityHandler := handlers.NewActivityHandler(store)
	protected.GET("/files/:id/activity", activityHandler.GetActivity)
	protected.POST("/files/:id/versions", activityHandler.CreateNamedSnapshot)
	protected.PUT("/files/:id/versions/:vid/label", activityHandler.LabelVersion)

	// OFFICE-26: comments (anchored, threaded, resolvable).
	commentHandler := handlers.NewCommentHandler(store)
	protected.GET("/files/:id/comments", commentHandler.List)
	protected.POST("/files/:id/comments", commentHandler.Create)
	protected.PUT("/files/:id/comments/:cid", commentHandler.Update)
	protected.DELETE("/files/:id/comments/:cid", commentHandler.Delete)
	protected.POST("/files/:id/comments/:cid/replies", commentHandler.CreateReply)
	protected.PUT("/files/:id/comments/:cid/replies/:rid", commentHandler.UpdateReply)
	protected.DELETE("/files/:id/comments/:cid/replies/:rid", commentHandler.DeleteReply)

	// OFFICE-27: suggestion / track-changes mode.
	suggestionHandler := handlers.NewSuggestionHandler(store)
	protected.GET("/files/:id/suggestions", suggestionHandler.List)
	protected.POST("/files/:id/suggestions", suggestionHandler.Create)
	protected.PUT("/files/:id/suggestions/:sid", suggestionHandler.Update)
	protected.DELETE("/files/:id/suggestions/:sid", suggestionHandler.Delete)

	// Docs export: PDF + DOCX server-side generation.
	docsExportHandler := handlers.NewDocsExportHandler(store)
	protected.GET("/files/:id/export", docsExportHandler.Export)

	uploadHandler := handlers.NewUploadHandler(cfg)
	protected.POST("/upload", uploadHandler.Upload)
	api.GET("/uploads/:filename", uploadHandler.Serve)

	localFilesHandler := handlers.NewLocalFilesHandler()
	protected.GET("/local-files", localFilesHandler.Scan)
	protected.GET("/local-files/serve", localFilesHandler.Serve)

	// OFFICE-63: short-lived TURN/ICE credentials for Vulos Spaces WebRTC calls.
	// Available authenticated (so creds aren't issued anonymously when auth is on).
	turnHandler := handlers.NewTURNHandler()
	protected.GET("/turn/credentials", turnHandler.Credentials)

	// OFFICE-41: envelope CRUD (field-placement setup).
	envelopeHandler := handlers.NewEnvelopeHandler(store)
	protected.GET("/envelopes", envelopeHandler.List)
	protected.GET("/envelopes/:id", envelopeHandler.Get)
	protected.POST("/envelopes", envelopeHandler.Create)
	protected.PUT("/envelopes/:id", envelopeHandler.Update)
	protected.DELETE("/envelopes/:id", envelopeHandler.Delete)

	// OFFICE-42: signing link generation + scoped signer view.
	// Send is protected (only the document owner can issue tokens).
	// GetSignerView and Complete are public — no Vulos account required.
	signingHandler := handlers.NewSigningHandler(store)
	protected.POST("/sign/:envelopeId/send", signingHandler.Send)
	api.GET("/sign/:token", signingHandler.GetSignerView)
	// OFFICE-43: signer ceremony submission.
	api.POST("/sign/:token/complete", signingHandler.Complete)

	// OFFICE-45: multi-signer orchestration + reminders.
	orchHandler := handlers.NewOrchestrationHandler(store)
	api.GET("/sign/:envelopeId/status", orchHandler.Status)
	protected.POST("/sign/:envelopeId/remind", orchHandler.Remind)
	protected.POST("/sign/:envelopeId/cancel", orchHandler.Cancel)
	api.POST("/sign/:token/decline", orchHandler.Decline)

	// OFFICE-46: sealed PDF download + audit manifest.
	// Protected: only the document owner/authenticated users may download.
	sealHandler := handlers.NewSealHandler(store, cfg.Server.UploadsDir)
	protected.GET("/sign/:envelopeId/download", sealHandler.Download)
	protected.GET("/sign/:envelopeId/manifest", sealHandler.Manifest)

	// OFFICE-47: signature + audit verification tool (public — no auth required).
	verifyHandler := handlers.NewVerifyHandler(store)
	api.POST("/sign/verify", verifyHandler.Verify)

	// SLIDES-07: slide deck PDF/PPTX export.
	slidesExportHandler := handlers.NewSlidesExportHandler(store)
	protected.GET("/slides/:id/export", slidesExportHandler.Export)

	// OFFICE-65: scheduled meeting rooms.
	meetingHandler := handlers.NewMeetingHandler(store)
	protected.POST("/meetings", meetingHandler.Create)
	protected.GET("/meetings", meetingHandler.List)
	protected.GET("/meetings/:id", meetingHandler.Get)
	protected.DELETE("/meetings/:id", meetingHandler.Delete)
	// Join is public — external invitees follow a bare link with no Vulos account.
	api.GET("/meetings/:id/join", meetingHandler.Join)

	// OFFICE-MEET: Google Meet parity — scheduled meetings, signed join tokens, lobby.
	meetScheduleHandler := handlers.NewMeetScheduleHandler(store)
	protected.POST("/meeting/schedule", meetScheduleHandler.Schedule)
	protected.GET("/meeting/schedule", meetScheduleHandler.List)
	protected.GET("/meeting/schedule/:id", meetScheduleHandler.Get)
	protected.DELETE("/meeting/schedule/:id", meetScheduleHandler.Delete)

	meetJoinHandler := handlers.NewMeetJoinHandler(meetScheduleHandler)
	// Token issuance: semi-public (anon token if no auth; signed-in token if auth present).
	api.POST("/meet/:roomId/token", meetJoinHandler.IssueToken)
	// Lobby endpoints: token required in header.
	api.POST("/meet/:roomId/lobby/enter", meetJoinHandler.LobbyEnter)
	protected.GET("/meet/:roomId/lobby", meetJoinHandler.LobbyList)
	protected.POST("/meet/:roomId/admit", meetJoinHandler.Admit)
	protected.POST("/meet/:roomId/admit-all", meetJoinHandler.AdmitAll)
	protected.POST("/meet/:roomId/deny", meetJoinHandler.Deny)

	// Sheets XLSX import/export endpoints.
	sheetsHandler := handlers.NewSheetsHandler(store)
	protected.POST("/sheets/:id/import", sheetsHandler.Import)
	protected.GET("/sheets/:id/export", sheetsHandler.Export)

	// Calendar: events, RSVP, ICS export, RRULE helper, subscriptions.
	calHandler := handlers.NewCalendarEventHandler()
	protected.GET("/calendar/events", calHandler.ListEvents)
	protected.POST("/calendar/events", calHandler.CreateEvent)
	protected.PUT("/calendar/events/:id", calHandler.UpdateEvent)
	protected.DELETE("/calendar/events/:id", calHandler.DeleteEvent)
	protected.POST("/calendar/events/:id/rsvp", calHandler.RSVPEvent)
	protected.GET("/calendar/export/:calID", calHandler.ExportICS)
	protected.POST("/calendar/rrule/expand", calHandler.ExpandRRule)

	calSubHandler := handlers.NewCalendarSubscribeHandler()
	protected.POST("/calendar/subscribe", calSubHandler.Subscribe)
	protected.GET("/calendar/subscriptions", calSubHandler.List)

	// Contacts: VCF import/export, dedup, merge.
	contactsHandler := handlers.NewContactsVCFHandler()
	protected.POST("/contacts/import", contactsHandler.ImportVCF)
	protected.GET("/contacts/export", contactsHandler.ExportVCF)
	protected.GET("/contacts/duplicates", contactsHandler.FindDuplicates)
	protected.POST("/contacts/merge", contactsHandler.MergeContacts)

	// Start background workers.
	handlers.StartReminderWorker(nil)
	handlers.StartSubscriptionRefresher()

	// OFFICE-60/61: Vulos Spaces — channels, DMs, threads, messages (CRDT-synced).
	// OFFICE-SPACES-1/4/5/6: reactions, status, search, pins (additive via SpacesHandlerExt).
	forumHandler := handlers.NewSpacesHandlerExt()
	protected.GET("/spaces/channels", forumHandler.ListChannels)
	protected.POST("/spaces/channels", forumHandler.CreateChannel)
	protected.POST("/spaces/channels/:channelId/join", forumHandler.JoinChannel)
	protected.GET("/spaces/channels/:channelId/members", forumHandler.ListMembers)
	protected.GET("/spaces/channels/:channelId/messages", forumHandler.ListMessages)
	protected.POST("/spaces/channels/:channelId/messages", forumHandler.SendMessage)
	protected.PUT("/spaces/channels/:channelId/messages/:msgId", forumHandler.EditMessage)
	protected.DELETE("/spaces/channels/:channelId/messages/:msgId", forumHandler.DeleteMessage)
	protected.POST("/spaces/channels/:channelId/read", forumHandler.MarkRead)
	protected.GET("/spaces/channels/:channelId/read", forumHandler.GetReadState)
	protected.GET("/spaces/channels/:channelId/ops", forumHandler.ExportOps)
	protected.POST("/spaces/ops", forumHandler.MergeOps)
	// Reactions (OFFICE-SPACES-1)
	protected.GET("/spaces/channels/:channelId/reactions", forumHandler.ListReactions)
	protected.POST("/spaces/messages/:msgId/react", forumHandler.React)
	protected.DELETE("/spaces/messages/:msgId/react", forumHandler.Unreact)
	// Pins (OFFICE-SPACES-6)
	protected.GET("/spaces/channels/:channelId/pins", forumHandler.ListPins)
	protected.POST("/spaces/channels/:channelId/pins", forumHandler.PinMessage)
	protected.DELETE("/spaces/channels/:channelId/pins/:msgId", forumHandler.UnpinMessage)
	// User status (OFFICE-SPACES-4)
	protected.PUT("/spaces/users/me/status", forumHandler.SetStatus)
	protected.GET("/spaces/users/:userId/status", forumHandler.GetStatus)
	// Search (OFFICE-SPACES-5)
	protected.GET("/spaces/channels/:channelId/search", forumHandler.SearchMessages)

	// Serve embedded frontend (SPA fallback to index.html)
	staticFS, err := fs.Sub(distFS, "dist")
	if err != nil {
		log.Fatal("Failed to create static FS:", err)
	}
	staticServer := http.FileServer(http.FS(staticFS))

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		// Try to open the path as a file in staticFS
		f, err := staticFS.Open(path)
		if err == nil {
			f.Close()
			staticServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		// SPA fallback: serve index.html
		c.Request.URL.Path = "/"
		staticServer.ServeHTTP(c.Writer, c.Request)
	})

	addr := cfg.Server.Addr
	if addr == "" {
		addr = ":8080"
	}

	log.Printf("Vulos Office running → http://localhost%s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}
}
